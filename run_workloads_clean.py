#!/usr/bin/env python3
import json
import os
import subprocess
import sys
import time

WORKLOADS = ["A", "B", "C", "D", "E", "F"]
RECORDS = 1000000
OPS = 100000
THREADS = 8

def main():
    print("========================================================================================")
    print(f"  STARTING 1M BENCHMARK: WORKLOAD ISOLATION & RAM CLEANUP (N={RECORDS:,} RECORDS)      ")
    print("  Each workload runs in an independent process; process terminates to free 100% of RAM.")
    print("========================================================================================\n")

    start_total = time.time()
    workload_results = []
    runs_results = []
    summary_results = []
    filter_stats = None

    for idx, wl in enumerate(WORKLOADS, 1):
        temp_out = f"ycsb_1m_temp_{wl}.json"
        cmd = [
            "./ycsb_suite_bin",
            "-runs", "1",
            "-records", str(RECORDS),
            "-ops", str(OPS),
            "-threads", str(THREADS),
            "-workload", wl,
            "-out", temp_out
        ]

        print(f"\n========================================================================================")
        print(f"  >>> [Workload {idx}/6: {wl}] Launching Fresh Process (RAM: Clean)...")
        print(f"  Command: {' '.join(cmd)}")
        print(f"========================================================================================")

        p = subprocess.run(cmd)
        if p.returncode != 0:
            print(f"ERROR: Workload {wl} failed with exit code {p.returncode}")
            sys.exit(p.returncode)

        print(f"\n  [Workload {wl}] Process exited. Process memory unmapped and returned to OS.")
        
        # Load partial result
        if os.path.exists(temp_out):
            with open(temp_out, "r") as f:
                d = json.load(f)
                if filter_stats is None and "filter_stats" in d:
                    filter_stats = d["filter_stats"]
                if "summary" in d and len(d["summary"]) > 0:
                    summary_results.extend(d["summary"])
                if "workloads" in d and len(d["workloads"]) > 0:
                    workload_results.extend(d["workloads"])
                if "runs" in d and len(d["runs"]) > 0 and len(d["runs"][0]["workloads"]) > 0:
                    runs_results.append(d["runs"][0]["workloads"][0])
            os.remove(temp_out)

        if idx < len(WORKLOADS):
            print(f"  Sleeping 5 seconds before Workload {WORKLOADS[idx]} to settle OS disk caches...")
            time.sleep(5)

    print("\n========================================================================================")
    print("  ALL 6 WORKLOADS COMPLETED! Consolidating Results into ycsb_1m_results.json...")
    print("========================================================================================")

    consolidated = {
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S+06:30"),
        "platform": "Apple Silicon (M-Series, 10-Core)",
        "record_count": RECORDS,
        "operation_count": OPS,
        "thread_count": THREADS,
        "total_runs": 1,
        "filter_stats": filter_stats,
        "summary": summary_results,
        "runs": [{
            "run_index": 1,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S+06:30"),
            "workloads": runs_results
        }],
        "workloads": workload_results
    }

    with open("ycsb_1m_results.json", "w") as f:
        json.dump(consolidated, f, indent=2)
    print("  Saved ycsb_1m_results.json successfully.")

    # Convert to Excel
    print("\n  Updating Excel workbooks...")
    try:
        from convert_1m_results import build_excel
        build_excel("ycsb_1m_results.json", "ycsb_benchmark_results_1m.xlsx")
        import shutil
        shutil.copy("ycsb_benchmark_results_1m.xlsx", "ycsb_1m_results.xlsx")
        print("  Updated ycsb_benchmark_results_1m.xlsx and ycsb_1m_results.xlsx!")
    except Exception as e:
        print(f"  Error building Excel: {e}")

    elapsed_total = time.time() - start_total
    print(f"\n========================================================================================")
    print(f"  TOTAL BENCHMARK DURATION: {elapsed_total:.2f}s ({elapsed_total/60:.2f} minutes)")
    print(f"========================================================================================")

if __name__ == "__main__":
    main()
