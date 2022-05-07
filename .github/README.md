# Workflows

## Create Personal Access Token (PAT):

- Goto https://github.com/settings/tokens
- Generate new token
- Note: WORKFLOW_PUBLIC_REPO
- Expiration: No expiration
- Select scopes: repo -> public_repo

## Add repo secret

- Goto Setting -> Secrects -> Actions
- New repository secret
- Name: WORKFLOW_PUBLIC_REPO_PAT
- Value: ghp_...