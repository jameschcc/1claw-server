#!/usr/bin/env python3
"""
Hermes Agent — one process per profile.
Communicates with Go server via JSON-over-stdin/stdout.

Protocol:
  Go → Agent:  {"type": "chat", "content": "Hello!", "id": "msg_001"}
  Agent → Go:  {"type": "reasoning", "content": "thinking...", "id": "msg_001"}
               {"type": "chat", "content": "Hi there!", "id": "msg_001"}
               {"type": "error", "code": "...", "message": "..."}
"""

import sys, json, os, signal, logging, threading

logging.basicConfig(level=logging.INFO, format="[agent-%(name)s] %(message)s", stream=sys.stderr)

# Keep original stdout for JSON protocol
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

    # Load env files
    load_env(os.path.join(hermes_home, ".env"))
    load_env(os.path.join(hermes_home, "profiles", profile_id, ".env"))

    # Add Hermes agent to path and import AIAgent
    agent_path = os.path.join(hermes_home, "hermes-agent")
    if os.path.isdir(agent_path) and agent_path not in sys.path:
        sys.path.insert(0, agent_path)

    try:
        from run_agent import AIAgent
        log.info("AIAgent imported for %s", profile_id)
    except ImportError as e:
        log.warning("AIAgent import failed: %s", e)
        send({"type": "error", "code": "IMPORT_FAILED", "message": str(e)})
        return

    # Read profile config
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

    # Create AIAgent with profile's own env
    _real_stdout = sys.stdout
    try:
        sys.stdout = sys.stderr
        agent = AIAgent(
            provider=provider,
            model=model,
            base_url=base_url,
            api_key=api_key,
            quiet_mode=True,
        )
    except Exception as e:
        log.error("AIAgent init failed: %s", e)
        send({"type": "error", "code": "INIT_FAILED", "message": str(e)})
        return
    finally:
        sys.stdout = _real_stdout

    log.info("Agent %s ready", profile_id)
    send({"type": "ready", "profile_id": profile_id})

    # Main loop: read from stdin, process chat
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError as e:
            send({"type": "error", "code": "PARSE_ERROR", "message": str(e)})
            continue

        cmd = msg.get("type")
        if cmd == "shutdown":
            break

        if cmd == "chat":
            content = msg.get("content", "")
            msg_id = msg.get("id", "")
            _real = sys.stdout
            try:
                sys.stdout = sys.stderr
                result = agent.run_conversation(content)
            except AttributeError:
                try:
                    sys.stdout = sys.stderr
                    result = {"final_response": agent.chat(content)}
                except Exception as e2:
                    send({"type": "error", "code": "CHAT_ERROR", "message": str(e2), "id": msg_id})
                    continue
            except Exception as e:
                send({"type": "error", "code": "CHAT_ERROR", "message": str(e), "id": msg_id})
                continue
            finally:
                sys.stdout = _real

            # Extract reasoning
            if isinstance(result, dict):
                reasoning = None
                for m in reversed(result.get("messages", [])):
                    if isinstance(m, dict) and m.get("role") == "assistant":
                        reasoning = m.get("reasoning")
                        if reasoning:
                            break
                if not reasoning:
                    reasoning = result.get("reasoning")
                if reasoning:
                    send({"type": "reasoning", "content": reasoning, "id": msg_id})
                final = result.get("final_response", result.get("content", str(result)))
            else:
                final = str(result)

            send({"type": "chat", "content": final, "id": msg_id})

if __name__ == "__main__":
    main()
