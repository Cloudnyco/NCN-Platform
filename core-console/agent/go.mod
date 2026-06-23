// ncn-agent — per-PoP telemetry agent. Tiny, single-binary, stdlib-only.
//
// Runs on every NCN PoP (pop-03, pop-04, ctrl-01, pop-06, fmt-01) and serves
// the same 14-segment shell-pipeline output that fleet.go on ctrl-01 used to
// fetch via SSH. Replaces the SSH-poll transport with HTTPS + HMAC-bearer.
//
// Standalone module (not pulled into core-console-api/backend) so the binary
// stays minimal and there's no risk of accidentally importing admin-side
// code into a process that runs on edge PoPs.
module github.com/ncn/agent

go 1.23
