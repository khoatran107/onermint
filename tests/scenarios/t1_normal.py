"""T1: normal run, no faults. All correct nodes finalize the same chain and state hash."""

from harness import scenario

scenario("t1-normal", network={}, count=300)
