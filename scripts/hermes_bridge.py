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
import threading

logging.basicConfig(
    level=logging.INFO,
    format="[hermes-bridge] %(message)s",
    stream=sys.stderr,
)
log = logging.getLogger("bridge")

# Keep original stdout for JSON protocol (immune to thread-local redirections)
_original_stdout = sys.stdout

# Defer heavy imports until init
AIAgent = None


def load_env_file(path):
    """Load a .env file into os.environ if it exists."""
    if not os.path.isfile(path):
        return False
    log.info("Loading env: %s", path)
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, _, val = line.partition("=")
            key = key.strip()
            val = val.strip().strip("\"'")
            if key and key not in os.environ:
                os.environ[key] = val
    return True


def find_hermes_home():
    """Find the Hermes home directory."""
    path = os.environ.get("HERMES_HOME")
    if path:
        return path
    path = os.path.expanduser("~/.hermes")
    if os.path.isdir(path):
        return path
    return None


def import_hermes():
    """Import AIAgent from the Hermes codebase."""
    global AIAgent
    if AIAgent is not None:
        return

    hermes_home = find_hermes_home()
    if not hermes_home:
        log.warning("Hermes home not found — using fallback")
        _init_echo_fallback()
        return

    # Load main .env first
    load_env_file(os.path.join(hermes_home, ".env"))
    load_env_file("/etc/environment")

    # Add Hermes agent to path
    agent_path = os.path.join(hermes_home, "hermes-agent")
    if os.path.isdir(agent_path) and agent_path not in sys.path:
        sys.path.insert(0, agent_path)

    try:
        from run_agent import AIAgent as HermesAIAgent
        AIAgent = HermesAIAgent
        log.info("AIAgent imported from %s", agent_path)
    except ImportError as e:
        log.warning("AIAgent import failed: %s — using fallback", e)
        _init_echo_fallback()


def _init_echo_fallback():
    """Set AIAgent to a simple echo agent."""
    global AIAgent
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

    def _init_echo(self, p):
        """Create an echo agent for the given profile dict."""
        pid = p.get("id", "")
        name = p.get("name", pid)
        echo = type("EchoAgent", (), {"chat": lambda self, m, n=name: f"[{n}] You said: {m}"})()
        self.agents[pid] = echo
        return echo

    def _load_profile_config(self, pid):
        """Load profile config.yaml and return model settings."""
        hermes_home = find_hermes_home()
        if not hermes_home:
            return {}
        cfg_path = os.path.join(hermes_home, "profiles", pid, "config.yaml")
        if pid == "default":
            cfg_path = os.path.join(hermes_home, "config.yaml")
        if not os.path.isfile(cfg_path):
            return {}
        try:
            import yaml
            with open(cfg_path) as f:
                cfg = yaml.safe_load(f)
            model_cfg = cfg.get("model", {}) if cfg else {}
            return {
                "provider": model_cfg.get("provider", ""),
                "model": model_cfg.get("default", ""),
                "base_url": model_cfg.get("base_url", ""),
                "api_key": model_cfg.get("api_key", ""),
            }
        except Exception as e:
            log.warning("Config load failed for %s: %s", pid, e)
            return {}

    def _init_real_agent(self, p):
        """Initialize a real AIAgent for a profile (runs in background thread)."""
        pid = p.get("id", "")
        name = p.get("name", pid)
        hermes_home = find_hermes_home()

        # Load profile-specific .env
        if hermes_home:
            profile_env = os.path.join(hermes_home, "profiles", pid, ".env")
            load_env_file(profile_env)

        # Load profile config for model settings
        cfg = self._load_profile_config(pid)

        # Tell AIAgent which profile it is (loads persona, AGENTS.md, memory)
        _old_profile = os.environ.get("HERMES_PROFILE")
        os.environ["HERMES_PROFILE"] = pid
        try:
            # Redirect AIAgent's stdout chatter to stderr
            _real_stdout = sys.stdout
            sys.stdout = sys.stderr
            agent = AIAgent(
                provider=cfg.get("provider", ""),
                model=cfg.get("model", "deepseek-v4-flash"),
                base_url=cfg.get("base_url", ""),
                api_key=cfg.get("api_key", None),
                quiet_mode=True,
            )
            self.agents[pid] = agent
        except Exception as e:
            log.warning("  ⚠️  %s real init failed: %s — keeping echo", pid, e)
            return
        finally:
            sys.stdout = _real_stdout
            if _old_profile is not None:
                os.environ["HERMES_PROFILE"] = _old_profile
            else:
                os.environ.pop("HERMES_PROFILE", None)

        self._send({"type": "agent_ready", "profile_id": pid, "status": "real"})
        log.info("  ✅ %s (%s) — real AI agent", name, pid)

    def handle_init(self, msg):
        """Initialize all agent profiles — echo immediately, real AI in background."""
        profiles = msg.get("profiles", [])
        log.info("Initializing %d profiles ...", len(profiles))

        import_hermes()
        hermes_home = find_hermes_home()

        for p in profiles:
            pid = p.get("id", "")
            if not pid:
                continue
            self.profiles[pid] = p
            # Echo agent immediately
            self._init_echo(p)

        # Load all profile envs for the background real-agent inits
        for p in profiles:
            pid = p.get("id", "")
            if pid and hermes_home:
                profile_env = os.path.join(hermes_home, "profiles", pid, ".env")
                load_env_file(profile_env)

        self.ready = True
        self._send({"type": "ready", "profile_count": len(self.agents)})
        log.info("Bridge ready — %d agents (echo). Initializing real AI in background...",
                 len(self.agents))

        # Start real AI agents in background
        for p in profiles:
            pid = p.get("id", "")
            if pid:
                t = threading.Thread(target=self._init_real_agent, args=(p,), daemon=True)
                t.start()

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
        _real_stdout = sys.stdout
        try:
            sys.stdout = sys.stderr
            response = agent.chat(content)
        except Exception as e:
            log.error("Chat error for %s: %s", pid, e)
            self._send({
                "type": "error",
                "code": "CHAT_ERROR",
                "message": str(e),
                "id": msg_id,
            })
            return
        finally:
            sys.stdout = _real_stdout

        self._send({
            "type": "chat",
            "profile_id": pid,
            "content": response,
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

    def handle_start_profile(self, msg):
        """Start a real AI agent for a specific profile (lazy init)."""
        pid = msg.get("profile_id", "")
        if not pid:
            self._send({"type": "error", "code": "MISSING_PROFILE", "message": "profile_id required"})
            return
        if pid not in self.profiles:
            self._send({"type": "error", "code": "PROFILE_NOT_FOUND", "message": f"Unknown profile: {pid}"})
            return

        # Check if already a real agent
        if pid in self.agents:
            self._send({"type": "agent_ready", "profile_id": pid, "status": "real"})
            return

        log.info("Lazy init: starting real agent for '%s' ...", pid)
        p = self.profiles[pid]
        t = threading.Thread(target=self._init_real_agent, args=(p,), daemon=True)
        t.start()
        self._send({"type": "agent_starting", "profile_id": pid})

    def _send(self, data):
        """Write a JSON line to the original stdout (thread-safe)."""
        line = json.dumps(data, ensure_ascii=False)
        _original_stdout.write(line + "\n")
        _original_stdout.flush()

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
            elif cmd == "start_profile":
                self.handle_start_profile(msg)
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
