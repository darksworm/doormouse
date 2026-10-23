# Contributing

When contributing to this repository, please first discuss the change you wish to make via issue, email, or any other method with the owners of this repository before making a change.

If you haven't let us know what/how/why you're working on, your contribution might get rejected, but please don't get discouraged - just get a 👍 from one of the maintainers in the issues section and you're ready to rock and roll.

If you'd like to contribute, but don't know where to start - check the issues labeled "good first issue".

🤖 AI aided contributions are welcome. Just make sure to add proper test coverage!

## Getting started
- Fork the repo and create a branch for your change.
- Make sure to document new features and changes in the README file.

## Commit messages
This project uses [Conventional Commits](https://www.conventionalcommits.org/).
Use the pattern `type(optional scope): short description` in the present tense.
Common types are:
- `feat` for a new feature
- `fix` for a bug fix
- `docs` for documentation changes
- `chore` for maintenance

Commit types decide the next version number, so pick them with care.
`feat` bumps the minor version and `fix` the patch version; `docs` and `chore`
do not trigger a release. Mark a breaking change with a `!` after the type, as
in `feat!: drop the old target syntax`.

## Releases
Releases are automatic. [release-please](https://github.com/googleapis/release-please)
reads the commits landing on `main` and keeps a release pull request open with
the next version and a generated changelog. Merging that pull request tags the
release and publishes the container image; nothing else needs doing by hand.

The pipeline needs one secret, `RELEASE_PLEASE_TOKEN`, a personal access token
with write access to contents, pull requests and issues. When it expires,
release pull requests simply stop appearing. Run
`scripts/rotate-release-token.sh` to set it up or replace it: it opens the form,
says what to tick, checks the token can actually write to the repository before
storing it, and prints the date it expires.

## Pull requests
- Rebase on the latest `main` branch before submitting.
- Keep commits focused; separate unrelated changes.
- Explain why the change is needed and reference issues when possible.

## Licensing
By contributing, you agree that your contributions will be licensed under the repository's GPL-3.0 license.
