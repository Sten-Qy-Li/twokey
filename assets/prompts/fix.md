# Your task: fix

Implement these confirmed findings from ${convergence_path}:
${fix_list}
${p3_rule}

1. Work on ${branch} at ${commit}. Follow the owner's decisions exactly.
2. Update the documentation each fix touches, including any audit or verification sections in the guides.
3. Run the project's checks: ${verify_commands}. Commit with clear messages and keep your usual Co-Authored-By trailer. One or more commits are fine.
4. Do not push. twokey checks your commits and pushes them.
5. Anything that needs a decision you can't make, or work outside the repository (GitLab settings, the cluster, teammates), goes in not_done with the reason. Don't attempt it.
6. Log entry: for each finding, what you changed and how you verified it; then what you did not do and why.

In your answer, base_commit is ${commit}, commits lists every commit you made (full SHAs, oldest first), and every finding above appears once, in done or in not_done.
