# lumniverse

See [Lumni](https://lumniverse.com) work, then connect your AI agents. Node 22+.

```bash
npx lumniverse try      # 1. see it work: sample agents make real mistakes, Lumni catches them
npx lumniverse init     # 2. connect your real agent (run it in the agent's folder)
```

Both open your browser; if you don't have an account yet, you create one there.

`init` and `try` use Lumni Cloud (https://app.lumniverse.com) unless you pass `--console https://app.yours.com` or set `LUMNI_CONSOLE_URL`.

- **`init`** opens your browser so you can approve the connection, then writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env` in the folder and prints the lines to add where your agent acts. With `--gateway PATH` it also wraps the local MCP servers it finds with Lumni's gateway, keeping a backup of each config.
- **`try`** asks your approval in the browser, runs about a dozen sample agents against sample apps in your workspace, then revokes the key it used. Watch Today and Flow while it runs.

To run Lumni yourself, use Docker Compose: [self-hosting guide](https://github.com/lumnihq/lumni/blob/main/docs/self-hosting.md).

SDKs: `npm i @lumniverse/sdk` · `pip install lumni-sdk`. License: Apache-2.0.
