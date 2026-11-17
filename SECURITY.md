# Security policy

## Supported versions

XTrader has no releases: only the latest `main` receives security fixes. Check that the problem still happens on it before reporting.

## Reporting a vulnerability

Do not open a public issue, discussion or pull request for a vulnerability.

Report it privately through GitHub: on the repository's **Security** tab, choose **Report a vulnerability**. Include:

- the affected component and the commit;
- the steps to reproduce it, or a proof of concept;
- the impact: what an attacker can read or do;
- any configuration it depends on.

Never put real credentials, tokens, MT5 logins or passwords, or data from a real account in a report: use fake values.

The report is answered in its private advisory. Keep the details private until a fix is published; the advisory can then be made public, crediting you if you wish.

## Scope

In scope, for example:

- the dashboard's authentication, sessions, CSRF protection, roles and permissions (`controller`, `web`);
- the internal API between the controller and the agents (`agentapi`) and its per-agent tokens;
- the handling of secrets: their encryption in the database, the export and import of the configuration file, and logs or API responses that could expose them;
- the MT5 bridge (`tools/mt5bridge`), its token and the orders sent through it;
- the authorization of the Telegram bot;
- the Docker deployment as shipped in `deploy/docker`.

Out of scope:

- trading losses and the performance of strategies, prompts or models (see the [risk disclaimer](README.md#risk-disclaimer));
- vulnerabilities of third parties (MetaTrader 5, the broker, the LLM providers, Telegram, ngrok), to be reported to them;
- attacks that need control of the host or of an administrator account;
- deployments that publish ports or turn protections off contrary to the documentation;
- volumetric denial of service.

## Running it safely

See [Security](README.md#security) in the README. In short:

- reach the dashboard only through HTTPS (a tunnel or a reverse proxy) and give every user a strong password;
- keep the published ports on the host's loopback, as the Docker deployment does;
- keep `XTRADER_SECRETS_KEY` and the `.env` files out of Git and backed up, and remove `XTRADER_BOOTSTRAP_USERNAME` and `XTRADER_BOOTSTRAP_PASSWORD` from the `.env` after the first login (both together: the controller refuses one without the other);
- test every change on a demo account; a real account needs **Permitir conta real** (Allow real account) on both the agent and the MT5 account.
