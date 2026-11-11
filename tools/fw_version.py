"""PlatformIO dynamic build flag: the firmware's version, from `git describe` (see platformio.ini)."""
import subprocess

try:
    v = subprocess.check_output(["git", "describe", "--tags", "--always", "--dirty"], stderr=subprocess.DEVNULL, text=True).strip()
except Exception:
    v = "dev"
print("'-DFW_VERSION=\"%s\"'" % v)
