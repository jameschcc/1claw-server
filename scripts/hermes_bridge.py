#!/usr/bin/env python3
"""
Hermes Bridge — long-lived subprocess for 1Claw server.
Communicates with Go server via JSON-over-stdin/stdout.

Protocol (line-delimited JSON, one message per line):

  Go → Python:
    {"type": "init", "profiles": [{"id": "assistant", "hermes_profile": "default"}, ...]}
    {"type": "chat", "profile_id": "assistant", "content": "Hello!", "id": "msg_001"}
    {"type": "status"}
    {"type": "shutdown"}

  Python → Go:
    {"type": "ready"}
    {"type": "chat", "profile_id": "assistant", "content": "Hi!", "id": "msg_001"}
    {"type": "status", "profiles": [...]}
    {"type": "error", "code": "...", "message": "..."}
"""

import sys
import json
import os
import signal
import logging

logging.basicConfig(
    level=logging.INFO,
    format="[hermes-bridge] %(message)s",
    stream=sys.stderr,
)
log = logging.getLogger("bridge")

# Defer heavy imports until init
AIAgent = None


def import_hermes():
    """Import AIAgent from the Hermes codebase."""
    global AIAgent
    if AIAgent is not None:
        return

    # Try standard Hermes install path
    hermes_home = os.environ.get("HERMES_HOME")
    if not hermes_home:
        hermes_home = os.path.expanduser("~/.hermes/hermes-agent")

    hermes_path = os.path.abspath(hermes_home)
    if hermes_path not in sys.path:
        sys.path.insert(0, hermes_path)

    try:
        from run_agent import AIAgent as HermesAIAgent
        AIAgent = HermesAIAgent
        log.info("AIAgent imported from %s", hermes_path)
    except ImportError as e:
        log.warning("AIAgent import failed: %s — using fallback", e)
        # Fallback: simple echo agent
        class EchoAgent:
            def __init__(self, **kwargs):
                self.name = kwargs.get("name", "Agent")
            def chat(self, message):
                return f"[{self.name}] You said: {message}"
        AIAgent = EchoAgent


class HermesBridge:
    def __init__(self):
        self.agents = {}  # profile_id → AIAgent instance
        self.profiles = {}  # profile_id → profile info
        self.ready = False

    def handle_init(self, msg):
        """Initialize all agent profiles."""
        profiles = msg.get("profiles", [])
        log.info("Initializing %d profiles ...", len(profiles))

        import_hermes()

        for p in profiles:
            pid = p.get("id", "")
            if not pid:
                continue
            self.profiles[pid] = p
            try:
                agent = AIAgent(
                    skip_memory=True,
                    skip_context_files=True,
                    quiet_mode=True,
                )
                self.agents[pid] = agent
                log.info("  ✅ %s (%s)", p.get("name", pid), pid)
            except Exception as e:
                log.warning("  ⚠️  %s init failed: %s — using echo fallback", pid, e)
                echo = type("EchoAgent", (), {"chat": lambda self, m, name=p.get("name",pid): f"[{name}] You said: {m}"})()
                self.agents[pid] = echo

        self.ready = True
        self._send({"type": "ready", "profile_count": len(self.agents)})
        log.info("Bridge ready — %d agents loaded", len(self.agents))

    def handle_chat(self, msg):
        """Route a chat message to the appropriate agent."""
        pid = msg.get("profile_id", "")
        content = msg.get("content", "")
        msg_id = msg.get("id", "")

        if pid not in self.agents:
            self._send({
                "type": "error",
                "code": "PROFILE_NOT_FOUND",
                "message": f"Profile '{pid}' not loaded",
                "id": msg_id,
            })
            return

        agent = self.agents[pid]
        try:
            response = agent.chat(content)
            self._send({
                "type": "chat",
                "profile_id": pid,
                "content": response,
                "id": msg_id,
            })
        except Exception as e:
            log.error("Chat error for %s: %s", pid, e)
            self._send({
                "type": "error",
                "code": "CHAT_ERROR",
                "message": str(e),
                "id": msg_id,
            })

    def handle_status(self, msg):
        """Return current status of all profiles."""
        profiles = []
        for pid, p in self.profiles.items():
            agent = self.agents.get(pid)
            alive = agent is not None
            profiles.append({
                "id": pid,
                "name": p.get("name", pid),
                "emoji": p.get("emoji", "🤖"),
                "description": p.get("description", ""),
                "color": p.get("color", "#0078D7"),
                "online": alive,
                "status": "working" if alive else "offline",
                "tasks_queue": 0,
            })
        self._send({"type": "status", "profiles": profiles})

    def _send(self, data):
        """Write a JSON line to stdout."""
        line = json.dumps(data, ensure_ascii=False)
        sys.stdout.write(line + "\n")
        sys.stdout.flush()

    def run(self):
        """Main loop: read JSON lines from stdin, dispatch."""
        log.info("Bridge started, waiting for commands...")
        for line in sys.stdin:
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError as e:
                self._send({"type": "error", "code": "PARSE_ERROR", "message": str(e)})
                continue

            cmd = msg.get("type", "")
            if cmd == "init":
                self.handle_init(msg)
            elif cmd == "chat":
                self.handle_chat(msg)
            elif cmd == "status":
                self.handle_status(msg)
            elif cmd == "shutdown":
                log.info("Shutdown requested")
                break
            else:
                self._send({"type": "error", "code": "UNKNOWN_COMMAND", "message": f"Unknown: {cmd}"})


def main():
    signal.signal(signal.SIGINT, lambda s, f: sys.exit(0))
    signal.signal(signal.SIGTERM, lambda s, f: sys.exit(0))
    bridge = HermesBridge()
    bridge.run()


if __name__ == "__main__":
    main()
