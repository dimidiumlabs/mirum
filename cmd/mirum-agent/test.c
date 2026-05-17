// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

#include "unity.h"

#include "mirum-agent.h"

void setUp(void) {}
void tearDown(void) {}

// Smoke test: the public entry point links and runs without crashing.
// Substantive suites (TLV decode, channel state machine) land with that
// code, exercising mirum-agent.h the same way.
static void test_mirum_init_runs(void) {
  mirum_init();
}

int main(void) {
  UNITY_BEGIN();
  RUN_TEST(test_mirum_init_runs);
  return UNITY_END();
}
