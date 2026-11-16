"""Exercise real PID namespaces and signals. Requires Docker and the demo image."""
import json
import os
import subprocess
import time
import unittest
import uuid

IMAGE = os.environ.get("DOCKTINEL_TEST_IMAGE", "docktinel-demo:local")


def docker(*args, check=True):
    return subprocess.run(["docker", *args], text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, timeout=30, check=check)


def eventually(fn, timeout=15):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = fn()
            if last:
                return last
        except (ValueError, subprocess.CalledProcessError):
            pass
        time.sleep(0.1)
    raise AssertionError(f"condition not met in {timeout}s; last result: {last!r}")


class Containers(unittest.TestCase):
    def setUp(self):
        self.name = "docktinel-test-" + uuid.uuid4().hex[:12]

    def tearDown(self):
        logs = docker("logs", self.name, check=False)
        print(f"\n{self.name}:\n{logs.stdout}{logs.stderr}")
        docker("rm", "-f", self.name, check=False)

    def start(self, script=None):
        args = ["run", "--init", "-d", "--name", self.name]
        if script:
            args += ["--entrypoint", "sh", IMAGE, "-ec", script]
        else:
            args += [IMAGE]
        docker(*args)
        eventually(lambda: self.status().get("phase") == "runtime")

    def status(self):
        return json.loads(docker("exec", self.name, "docktinel", "status", "--json").stdout)

    def running(self):
        return docker("inspect", "-f", "{{.State.Running}}", self.name).stdout.strip() == "true"

    def test_critical_check_stops_container(self):
        self.start()
        docker("exec", self.name, "docktinel", "healthcheck")
        docker("exec", self.name, "touch", "/srv/fail-health")
        eventually(lambda: not self.running())

    def test_degraded_dependency_recovers(self):
        self.start('''
            docktinel daemon --heartbeat 100ms --startup-timeout 10s &
            docktinel add check optional --phase both --deadline 200ms --interval 100ms \
                --failure degrade -- test -f /tmp/available
            docktinel startup
            exec docktinel exec -- python -c 'import time; time.sleep(600)'
        ''')
        eventually(lambda: self.status()["state"] == "degraded")
        docker("exec", self.name, "docktinel", "healthcheck")
        docker("exec", self.name, "touch", "/tmp/available")
        eventually(lambda: self.status()["state"] == "healthy")

    def test_startup_deadline_stops_unfinished_entrypoint(self):
        docker("run", "--init", "-d", "--name", self.name, "--entrypoint", "sh", IMAGE, "-ec", """
            docktinel daemon --heartbeat 100ms --startup-timeout 300ms --stop-grace 200ms &
            sleep 600
        """)
        eventually(lambda: not self.running())

    def test_lost_snapshot_stops_main(self):
        self.start()
        docker("exec", self.name, "kill", "-STOP", str(self.status()["daemon"]["pid"]))
        docker("exec", self.name, "rm", "/run/docktinel/status.json")
        self.assertNotEqual(docker("exec", self.name, "docktinel", "healthcheck", check=False).returncode, 0)
        eventually(lambda: not self.running())

    def test_frozen_watchdog_escalates_term_to_kill(self):
        self.start('''
            docktinel daemon --heartbeat 100ms --startup-timeout 10s --stop-grace 200ms &
            docktinel startup
            exec docktinel exec -- python -c \
                'import signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); time.sleep(600)'
        ''')
        time.sleep(0.3)  # allow the application's TERM handler to be installed
        pid = self.status()["daemon"]["pid"]
        docker("exec", self.name, "kill", "-STOP", str(pid))
        time.sleep(0.3)
        args = ("exec", self.name, "docktinel", "healthcheck", "--max-age", "200ms", "--stop-grace", "200ms")
        self.assertNotEqual(docker(*args, check=False).returncode, 0)
        self.assertTrue(self.running(), "first failure must send TERM, allowing a grace period")
        time.sleep(0.3)
        docker(*args, check=False)
        eventually(lambda: not self.running())
        self.assertEqual(docker("inspect", "-f", "{{.State.ExitCode}}", self.name).stdout.strip(), "137")


if __name__ == "__main__":
    unittest.main(verbosity=2)
