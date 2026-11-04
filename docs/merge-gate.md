# Merge gate on main

This document describes the merge gate protecting the `main` branch, how repository administrators apply it, how to verify it is live, and workflow constraints required to keep it working.

## What the gate enforces

The merge gate is defined by the GitHub ruleset in `.github/rulesets/main.json` named `"main merge gate"` targeting `~DEFAULT_BRANCH` with enforcement set to active:

- **Pull request required**: Direct pushes to `main` are blocked; all changes must arrive via a pull request. Approval count is set to 0 (`required_approving_review_count: 0`), so self-merges without external reviews are permitted once CI passes.
- **Required status check (`ci`)**: The `ci` status check must pass before a pull request can merge. A failed, queued, in-progress, or never-reported `ci` check blocks merging.
- **Branch does not need to be up to date**: The strict status check policy is off (`strict_required_status_checks_policy: false`), so branches do not need to be rebased or updated against `main` if `main` has advanced since the branch was cut, as long as `ci` passed on the branch.
- **Block force pushes and deletion**: Non-fast-forward updates (`non_fast_forward`) and branch deletion (`deletion`) of `main` are blocked.
- **Bypass permissions**: Repository administrators (`RepositoryRole` id 5) can bypass the ruleset (`bypass_mode: "always"`). All other contributors and roles are strictly bound.
- **Fork PR protection**: Pull requests from forks are subject to the same `ci` gate. The `.github/workflows/ci.yml` workflow triggers on `pull_request` (not `pull_request_target`) and references no secrets, ensuring fork PRs run the required checks securely without risking credential exposure.

## Admin apply steps

### Precondition
Before importing or applying the ruleset, ensure `main`'s latest `ci` workflow run is green (specifically that the lint fix to `web/login_focus_order_test.go` and this PR have merged into `main`). Do not apply the ruleset while `main` is broken.

### Option A: GitHub Web UI
1. In the repository on GitHub, navigate to **Settings** → **Rules** → **Rulesets**.
2. Click **New ruleset** → **Import a ruleset**.
3. Choose `.github/rulesets/main.json` from the repository.
4. Verify the imported settings and click **Create** / **Save changes**.

### Option B: GitHub CLI / API
Run the following command using a personal access token or authentication with repository admin rights:

```bash
gh api -X POST repos/softsrv/pixelpace/rulesets --input .github/rulesets/main.json
```

## Confirming the gate is live

Once applied, verify that the merge gate is active:

1. **Check rulesets list via API**:
   ```bash
   gh api repos/softsrv/pixelpace/rulesets
   ```
   Verify that `"main merge gate"` appears in the list with enforcement status `active`.

2. **Inspect ruleset details**:
   ```bash
   gh api repos/softsrv/pixelpace/rulesets/<id>
   ```
   Verify that `conditions.ref_name.include` targets `~DEFAULT_BRANCH` and `rules` includes `required_status_checks` with context `"ci"`.

3. **Verify merge button blocking**:
   - Open a pull request with a failing or deliberately broken change.
   - Observe that while `ci` is pending or after `ci` fails, the merge button is blocked.
   - Non-admin direct pushes (`git push origin main`) must be rejected.
   - An administrator should see a bypass option when viewing a blocked PR.

*Note: If the platform or automation token lacks permission to read repository rulesets via `gh api`, record the administrator's manual confirmation.*

## Warning: CI workflow job ID

The required status check context matches the job identifier `ci` defined in `.github/workflows/ci.yml`.

**Do not rename the `ci` job or add a `name:` field to it in `.github/workflows/ci.yml`.** Adding a `name:` property or changing the job key changes the GitHub status check context string emitted by GitHub Actions, which would permanently mismatch the ruleset and block all pull requests from merging.
