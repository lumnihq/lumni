# lumniverse

Connect your AI agents to [Lumni](https://lumniverse.com), see it work with sample agents, or look around first. Node 22+.

```bash
npx lumniverse init     # connect the agent in this folder (opens your browser)
npx lumniverse try      # sample agents act out twelve problems in your workspace
npx lumniverse demo     # the console on a made-up day: no account
```

`init` and `try` use Lumni Cloud (https://app.lumniverse.com) unless you pass `--console https://app.yours.com` or set `LUMNI_CONSOLE_URL`.

- **`init`** opens your browser so you can approve the connection, then writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env` in the folder and prints the lines to add where your agent acts. With `--gateway PATH` it also wraps the local MCP servers it finds with Lumni's gateway, keeping a backup of each config.
- **`try`** asks your approval in the browser, runs about a dozen sample agents against demo apps in your workspace, then revokes the key it used.
- **`demo`** serves the console on your machine with a simulated day. No API, no account.

To run Lumni yourself, use Docker Compose: [self-hosting guide](https://github.com/lumnihq/lumni/blob/main/docs/self-hosting.md).

SDKs: `npm i @lumniverse/sdk` · `pip install lumni-sdk`. License: Apache-2.0.
