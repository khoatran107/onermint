"""Run Go unit tests, static checks and every real-process T1-T8 scenario script."""

import argparse
from datetime import datetime
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

REPO = Path(__file__).resolve().parent.parent


def main():
    if len(sys.argv) == 1:
        print("Run suite: python tests/run_tests.py --output local-runs/test-evidence")
        print("Add --race for race detection. The suite can take several minutes.")
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable or full path")
    parser.add_argument("--race", action="store_true", help="Include Go race detection")
    parser.add_argument("--output", type=Path, help="Fresh evidence directory")
    args = parser.parse_args()
    go = shutil.which(args.go)
    if go is None:
        raise ValueError(f"Go executable not found: {args.go}")
    output = (args.output or REPO / "local-runs" / datetime.now().strftime("suite-%Y%m%d-%H%M%S-%f")).resolve()
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, LAB01_EVIDENCE_DIR=str(output))
    print(f"Evidence directory: {output}", flush=True)

    # Check formatting without changing student files.
    gofmt = Path(go).with_name("gofmt.exe" if os.name == "nt" else "gofmt")
    formatting = subprocess.run([str(gofmt), "-l", "src"], cwd=REPO,
                                capture_output=True, text=True, check=True)
    if formatting.stdout.strip():
        raise ValueError(f"Go files need formatting:\n{formatting.stdout}")
    print("Formatting: clean", flush=True)

    command = [go, "test", "./...", "-count=1", "-json"]
    if args.race:
        command.append("-race")
    passed = set()
    failures = []
    skipped = []
    processes = None
    try:
        with (output / "go-test.jsonl").open("w", encoding="utf-8") as log:
            processes = subprocess.Popen(command, cwd=REPO, env=env, stdout=subprocess.PIPE,
                                         stderr=subprocess.STDOUT, text=True, encoding="utf-8")
            for line in processes.stdout:
                log.write(line)
                try:
                    event = json.loads(line)
                except json.JSONDecodeError:
                    print(line.rstrip(), flush=True)
                    continue
                name = event.get("Test", "")
                action = event.get("Action")
                if action == "pass" and name:
                    passed.add(name)
                elif action == "fail":
                    failures.append(name or event.get("Package"))
                    print(f"FAIL {failures[-1]}", flush=True)
                elif action == "skip" and name:
                    skipped.append(name)
                if action == "output" and ("Error:" in event.get("Output", "") or "--- FAIL" in event.get("Output", "")):
                    print(event["Output"].rstrip(), flush=True)
            code = processes.wait()
    finally:
        if processes is not None and processes.poll() is None:
            processes.terminate()
            try:
                processes.wait(timeout=10)
            except subprocess.TimeoutExpired:
                processes.kill()
                processes.wait()
        if processes is not None and processes.stdout is not None:
            processes.stdout.close()

    summary = {"race": args.race, "exit_code": code, "passed_tests_and_subtests": len(passed),
               "failed": failures, "skipped_helpers": skipped}
    (output / "summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
    if code or failures:
        raise RuntimeError(f"Go tests failed; inspect {output / 'go-test.jsonl'} and summary.json")

    vet = subprocess.run([go, "vet", "./..."], cwd=REPO, capture_output=True, text=True)
    (output / "go-vet.log").write_text(vet.stdout + vet.stderr, encoding="utf-8")
    summary["vet_exit_code"] = vet.returncode
    if vet.returncode:
        (output / "summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
        raise RuntimeError(f"Static checks failed: {vet.stderr}")
    print(f"Go tests passed ({len(passed)} tests/subtests); static checks passed.", flush=True)

    # Each scenario starts its own 10-process cluster, so they run one after another.
    scenarios = {}
    for script in sorted((REPO / "tests" / "scenarios").glob("t*.py")):
        print(f"== {script.name}", flush=True)
        with (output / f"{script.stem}.log").open("w", encoding="utf-8") as log:
            run = subprocess.Popen([sys.executable, str(script)], cwd=REPO, env=dict(env, LAB01_GO=go),
                                   stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, encoding="utf-8")
            for line in run.stdout:
                log.write(line)
                if line.lstrip().startswith(("ok", "FAIL", "RESULT")):
                    print(line.rstrip(), flush=True)
            scenarios[script.stem] = run.wait()
    summary["scenarios"] = {name: ("PASS" if code == 0 else "FAIL") for name, code in scenarios.items()}
    (output / "summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
    bad = [name for name, code in scenarios.items() if code]
    if bad:
        raise RuntimeError(f"Scenarios failed: {', '.join(bad)}; see {output}/<scenario>.log")
    print(f"All {len(scenarios)} scenarios passed; evidence in {output}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("Interrupted; suite was not completed.", file=sys.stderr)
        sys.exit(130)
