#!/usr/bin/env python3
import argparse
import hashlib
import json
import sys

import cv2


def digest(frame):
    return hashlib.sha256(frame.tobytes()).digest()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--video", required=True)
    parser.add_argument("--samples", type=int, default=60)
    args = parser.parse_args()
    if args.samples < 1:
        raise ValueError("samples must be positive")
    cap = cv2.VideoCapture(args.video)
    if not cap.isOpened():
        raise ValueError("cannot open video")
    try:
        total = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))
        if total < 1:
            raise ValueError("cannot determine frame count")
        count = min(args.samples, total)
        targets = sorted({round((total - 1) * i / max(count - 1, 1)) for i in range(count)})
        expected = {}
        wanted = set(targets)
        for index in range(targets[-1] + 1):
            ok, frame = cap.read()
            if not ok:
                raise ValueError(f"sequential read failed at frame {index}")
            if index in wanted:
                expected[index] = digest(frame)
    finally:
        cap.release()
    cap = cv2.VideoCapture(args.video)
    mismatches = []
    try:
        for index in targets:
            cap.set(cv2.CAP_PROP_POS_FRAMES, index)
            ok, frame = cap.read()
            if not ok or digest(frame) != expected[index]:
                mismatches.append(index)
    finally:
        cap.release()
    print(json.dumps({"samples": len(targets), "mismatches": mismatches}))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
