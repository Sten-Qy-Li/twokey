# Your task: blind audit (${agent_name})

Audit the repository at commit ${commit}. The question: is it ready for ${checkpoint_label}?

This audit is blind. ${other_agent} is auditing the same commit right now; you won't see its work in this step, and it won't see yours.

1. Read the requirements and every source they list.
2. Check code, tests, configuration and documentation against each requirement. Run what you need to run, in a copy under your scratch folder whenever it would write anything.
3. If earlier rounds of this run fixed findings (see History), check that each fix works.
4. Give each finding an ID ${id_prefix}-NN (for example ${id_prefix}-01), a severity, a kind, evidence and a fix hint.
5. Write your log entry in the style of earlier entries: Task, Revision, Verdict, Method, findings in priority order, what you checked and what you could not check. Briefly credit what is done well.

verdict = "ready_for_human_review" only if you have no P1/P2 repo_fix or needs_decision findings; otherwise "not_ready".
