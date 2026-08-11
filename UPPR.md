# Prepare this application for Uppr

This file is an implementation brief for an AI coding agent. Inspect this project, preserve its existing framework and behavior, and adapt it to the Uppr application contract below. Do not put real credentials, generated databases, or other runtime state in source control.

## Required project layout

Create or verify this structure at the repository root:

~~~text
.
├── Dockerfile
├── schema.json
├── config/
│   └── .env          # runtime environment values; never commit secrets
└── data/
    └── main.sqlite   # primary persistent SQLite database
~~~

The names and locations are part of the contract. Uppr bind-mounts the host project's `config/` directory at `/app/config` and `data/` at `/app/data` in the container. The application must therefore read environment variables from `/app/config/.env` and use `/app/data/main.sqlite` when it runs in Docker. For local execution, use the equivalent repository-relative paths `config/.env` and `data/main.sqlite`.

## Environment configuration

- Load application settings from `config/.env`. Do not require a root-level `.env` file.
- Keep `config/.env` and all other files in `config/` out of Git.
- Commit a root-level `schema.json` that documents every supported environment variable. Uppr uses it to create missing blank entries without overwriting existing values.
- The preferred schema shape is:

~~~json
{
  "variables": [
    {
      "name": "APP_ENV",
      "description": "Runtime mode used by the application.",
      "example": "production",
      "required": true
    }
  ]
}
~~~

Use `name` for each variable key. Give each entry a useful `description`, a safe non-secret `example`, and an accurate `required` boolean. The legacy root-level `env.schema` format (one variable name per line) is supported, but new integrations should use `schema.json`.

## Persistent data

- Store the primary SQLite database at `data/main.sqlite` locally and `/app/data/main.sqlite` in the container.
- Store uploads and any other durable application state under `data/` so it survives container replacement.
- Create parent directories on startup when necessary. Do not store durable state only in the container filesystem.
- Ignore all of `data/` in Git. If the application does not use SQLite, still tolerate the Uppr-created `data/main.sqlite` file and keep all persistent state under `data/`.

## Docker contract

- Commit a production-ready root-level `Dockerfile`.
- Include a numeric `EXPOSE <port>` instruction. Uppr reads the first valid numeric exposed port and uses it as the default host and container port. Do not use only a variable such as `EXPOSE ${PORT}`.
- Make the application listen on `0.0.0.0` inside the container, not only `127.0.0.1`.
- The container startup command must start the application directly and remain in the foreground.
- Ensure the configured application port matches the first numeric `EXPOSE` port.
- Do not bake `config/.env` or `data/` into the image. Add both to `.dockerignore`.
- The image must work with the Uppr mounts at `/app/config` and `/app/data`.

## Source-control safety

Add these protected paths to `.gitignore` (preserve any existing rules):

~~~gitignore
config/
data/
~~~

Remove any tracked runtime secrets or database files from the Git index while preserving the local files. Never invent or commit credential values. A checked-in example file may contain safe placeholders, but `schema.json` is the canonical machine-readable environment reference.

## Completion checklist

Before declaring the integration complete:

1. Verify the application uses `config/.env` and no root `.env` dependency remains.
2. Verify durable state is written under `data/` and the primary SQLite path is `data/main.sqlite`.
3. Validate `schema.json` as JSON and confirm it covers every environment variable read by the code.
4. Build the root `Dockerfile` and confirm its first numeric `EXPOSE` matches the listening port.
5. Run the container with `config/` mounted to `/app/config` and `data/` mounted to `/app/data`; verify it starts and accepts traffic.
6. Confirm `config/` and `data/` are ignored and no secrets or runtime database files are tracked.
7. Run the project's existing tests and report any unrelated failures separately.

When finished, summarize the files changed, the exposed port, the environment variables documented, the persistence paths used, and the verification commands run.
