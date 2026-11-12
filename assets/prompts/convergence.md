# Your task: convergence check

Two blind audits of commit ${commit} are finished:
- ${codex_audit_path}
- ${claude_audit_path}
One of them is your own earlier audit. Treat it with the same scepticism as the other.

Upstream branch: ${upstream_status}

1. Independently verify every finding that only one audit raised, P1 and P2 first. Reproduce where you can.
2. Merge findings that both audits raised.
3. Use what you learned to look for related problems neither audit found. Add them with source "convergence".
4. Give every blocking finding (P1/P2, repo_fix or needs_decision) from either audit a status:
   - confirmed: real, and still P1 or P2;
   - disputed: wrong, or not P1/P2. Give dispute_reason with evidence.
   Use "noted" for P3 and external_evidence items. If a finding repeats a problem that an earlier round fixed, set recurs_of to that earlier ID (earlier merged lists: ${prior_merged_paths}).
5. Use IDs ${id_prefix}-NN for merged findings, with merged_from listing the original IDs.
6. Log entry: what each audit got right, what it got wrong, what both missed, and the merged list.
