import sys
from pathlib import Path
from urllib.error import URLError
from urllib.request import urlopen

if Path("/srv/fail-health").exists():
    sys.exit("simulated health failure")
try:
    with urlopen("http://127.0.0.1:8080/", timeout=1) as response:
        if response.status != 200:
            sys.exit("unexpected HTTP response")
except (URLError, TimeoutError) as error:
    sys.exit(f"HTTP not ready: {error}")
