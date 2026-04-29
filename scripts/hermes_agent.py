#!/usr/bin/env python3
"""
Hermes Agent — one process per profile with multiple persistent sessions.

Each inbound chat carries a session_id. The wrapper reuses one AIAgent per
session so Hermes can keep short-term state in memory, and also attaches a
SessionDB so conversations survive agent/server restarts without replaying full
history from the client.
"""

import json
import logging
import os
import signal
import sys
import threading

logging.basicConfig(level=logging.INFO, format="[agent-%(name)s] %(message)s", stream=sys.stderr)

_stdout = sys.stdout


def load_env(path):
    if os.path.isfile(path):
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                k, _, v = line.partition("=")
                k, v = k.strip(), v.strip().strip("\"'")
                if k and k not in os.environ:
                    os.environ[k] = v


def send(data):
    line = json.dumps(data, ensure_ascii=False)
    _stdout.write(line + "\n")
    _stdout.flush()


def main():
    signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))
    signal.signal(signal.SIGTERM, lambda s, f: sys.exit(0))

    profile_id = os.environ.get("HERMES_PROFILE", "default")
    hermes_home = os.environ.get("HERMES_HOME") or os.path.expanduser("~/.hermes")
    log = logging.getLogger(profile_id)

    load_env(os.path.join(hermes_home, ".env"))
    load_env(os.path.join(hermes_home, "profiles", profile_id, ".env"))

    agent_path = os.path.join(hermes_home, "hermes-agent")
    if os.path.isdir(agent_path) and agent_path not in sys.path:
        sys.path.insert(0, agent_path)

    try:
        from run_agent import AIAgent
        log.info("AIAgent imported for %s", profile_id)
    except ImportError as e:
        log.warning("AIAgent import failed: %s", e)
        send({"type": "error", "code": "IMPORT_FAILED", "message": str(e), "profile_id": profile_id})
        return

    try:
        from hermes_state import SessionDB
        session_db = SessionDB()
    except Exception as e:
        session_db = None
        log.warning("SessionDB unavailable: %s", e)

    cfg_path = os.path.join(hermes_home, "profiles", profile_id, "config.yaml")
    if profile_id == "default":
        cfg_path = os.path.join(hermes_home, "config.yaml")

    provider, model, base_url, api_key = "", "deepseek-v4-flash", "", None
    if os.path.isfile(cfg_path):
        try:
            import yaml

            with open(cfg_path) as f:
                mc = yaml.safe_load(f).get("model", {})
            provider = mc.get("provider", "")
            model = mc.get("default", "deepseek-v4-flash")
            base_url = mc.get("base_url", "")
            api_key = mc.get("api_key", None)
        except Exception as e:
            log.warning("Config load failed: %s", e)

    session_agents = {}
    session_agents_lock = threading.Lock()
    active_run_lock = threading.Lock()
    active_run = None

    def normalize_history(history):
        normalized = []
        for entry in history or []:
            if not isinstance(entry, dict):
                continue
            role = str(entry.get("role", "")).strip()
            content = entry.get("content", "")
            if not isinstance(content, str):
                continue
            content = content.strip()
            if not content:
                continue
            normalized.append({"role": role, "content": content})
        return normalized

    def create_agent(session_id):
        _real_stdout = sys.stdout
        try:
            sys.stdout = sys.stderr
            return AIAgent(
                provider=provider,
                model=model,
                base_url=base_url,
                api_key=api_key,
                quiet_mode=True,
                session_id=session_id,
                session_db=session_db,
            )
        finally:
            sys.stdout = _real_stdout

    def get_or_create_agent(session_id):
        with session_agents_lock:
            agent = session_agents.get(session_id)
            created = False
            if agent is None:
                agent = create_agent(session_id)
                session_agents[session_id] = agent
                created = True
            return agent, created

    def load_persisted_history(session_id):
        if session_db is None or not session_id:
            return []
        try:
            return normalize_history(session_db.get_messages_as_conversation(session_id) or [])
        except Exception as e:
            log.warning("Failed to load session history for %s: %s", session_id, e)
            return []

    def clear_active_run(run_state):
        nonlocal active_run
        with active_run_lock:
            if active_run is run_state:
                active_run = None

    def run_chat(agent, session_id, content, msg_id, history, created, run_state):
        try:
            try:
                agent.clear_interrupt()
            except Exception:
                pass

            conversation_history = None
            if created:
                persisted_history = load_persisted_history(session_id)
                if persisted_history:
                    conversation_history = persisted_history
                elif history:
                    conversation_history = history

            # Streaming: accumulate text deltas and send per-chunk "chat" messages
            streamed_chunks = []

            def on_stream_delta(text: str):
                streamed_chunks.append(text)
                full_text = ''.join(streamed_chunks)
                send({
                    "type": "chat_chunk",
                    "content": full_text,
                    "id": msg_id,
                    "profile_id": profile_id,
                    "session_id": session_id,
                })

            def on_reasoning_delta(text: str):
                send({
                    "type": "reasoning",
                    "content": text,
                    "id": msg_id,
                    "profile_id": profile_id,
                    "session_id": session_id,
                })

            agent.reasoning_callback = on_reasoning_delta

            _real_stdout = sys.stdout
            try:
                sys.stdout = sys.stderr
                result = agent.run_conversation(
                    content,
                    conversation_history=conversation_history,
                    stream_callback=on_stream_delta,
                )
            finally:
                sys.stdout = _real_stdout
        except Exception as e:
            if not run_state.get("cancelled"):
                send({
                    "type": "error",
                    "code": "CHAT_ERROR",
                    "message": str(e),
                    "id": msg_id,
                    "profile_id": profile_id,
                    "session_id": session_id,
                })
            clear_active_run(run_state)
            try:
                agent.clear_interrupt()
            except Exception:
                pass
            return

        clear_active_run(run_state)

        try:
            agent.clear_interrupt()
        except Exception:
            pass

        if run_state.get("cancelled"):
            return

        # Reasoning and final chat were already streamed via callbacks.
        # Send the final complete response to guarantee delivery.
        if isinstance(result, dict):
            final = result.get("final_response", result.get("content", str(result)))
        else:
            final = str(result)

        send({
            "type": "chat",
            "content": final,
            "id": msg_id,
            "profile_id": profile_id,
            "session_id": session_id,
        })

    log.info("Agent %s ready", profile_id)
    send({"type": "ready", "profile_id": profile_id})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            msg = json.loads(line)
        except json.JSONDecodeError as e:
            send({"type": "error", "code": "PARSE_ERROR", "message": str(e), "profile_id": profile_id})
            continue

        cmd = msg.get("type")
        if cmd == "shutdown":
            break

        if cmd == "cancel":
            session_id = str(msg.get("session_id", "")).strip()
            with active_run_lock:
                run_state = active_run
                if run_state is None:
                    send({
                        "type": "error",
                        "code": "NO_ACTIVE_RESPONSE",
                        "message": "No active response to cancel",
                        "profile_id": profile_id,
                        "session_id": session_id,
                    })
                    continue
                if session_id and run_state["session_id"] != session_id:
                    send({
                        "type": "error",
                        "code": "SESSION_MISMATCH",
                        "message": "Active response belongs to a different session",
                        "profile_id": profile_id,
                        "session_id": session_id,
                    })
                    continue
                run_state["cancelled"] = True
                try:
                    run_state["agent"].interrupt()
                except Exception as e:
                    send({
                        "type": "error",
                        "code": "CANCEL_FAILED",
                        "message": str(e),
                        "profile_id": profile_id,
                        "session_id": run_state["session_id"],
                        "id": run_state["message_id"],
                    })
                    continue

            send({
                "type": "cancelled",
                "profile_id": profile_id,
                "session_id": run_state["session_id"],
                "id": run_state["message_id"],
                "message": "Response cancelled",
            })
            continue

        if cmd != "chat":
            continue

        content = str(msg.get("content", ""))
        msg_id = str(msg.get("id", "")).strip()
        session_id = str(msg.get("session_id", "")).strip() or f"{profile_id}_{msg_id}"
        bootstrap_history = normalize_history(msg.get("history"))

        with active_run_lock:
            if active_run is not None and active_run["thread"].is_alive():
                send({
                    "type": "error",
                    "code": "AGENT_BUSY",
                    "message": "Agent is already processing another request",
                    "id": msg_id,
                    "profile_id": profile_id,
                    "session_id": session_id,
                })
                continue

        try:
            agent, created = get_or_create_agent(session_id)
        except Exception as e:
            send({
                "type": "error",
                "code": "INIT_FAILED",
                "message": str(e),
                "id": msg_id,
                "profile_id": profile_id,
                "session_id": session_id,
            })
            continue

        run_state = {
            "agent": agent,
            "session_id": session_id,
            "message_id": msg_id,
            "cancelled": False,
        }
        worker = threading.Thread(
            target=run_chat,
            args=(agent, session_id, content, msg_id, bootstrap_history, created, run_state),
            name=f"chat-{profile_id}-{msg_id}",
            daemon=True,
        )
        run_state["thread"] = worker

        with active_run_lock:
            active_run = run_state

        worker.start()


if __name__ == "__main__":
    main()
