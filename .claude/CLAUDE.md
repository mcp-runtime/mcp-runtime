# graphify

Use the Graphify CLI when `graphify-out/graph.json` exists or the user asks to
build/query a knowledge graph. There is no repo-local graphify skill.

```bash
graphify query "<question>"
graphify path "<A>" "<B>"
graphify explain "<concept>"
graphify update .
```

When the user types `/graphify`, run the Graphify CLI for the requested path
(or current directory) rather than loading a skill.
