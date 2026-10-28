# Code signing (maintainers)

Windows release installers are Authenticode-signed through [SignPath Foundation](https://signpath.org), which signs open-source projects for free. Signing runs in `.github/workflows/build-desktop.yml` and stays off until the repository is configured; until then every Windows release build finishes with an "unsigned" warning instead of failing.

## What the workflow does

On a `v*` tag, for each Windows installer (x64 and arm64), when the repository variable `SIGNPATH_ORGANIZATION_ID` is set:

1. `pnpm build` writes `Luxury-Switchboard-<version>-windows-<arch>-setup.exe`.
2. The unsigned installer is uploaded as a short-lived `unsigned-switchboard-windows-<arch>` artifact. The name deliberately does not match `switchboard-*`, so `collect` can never pick up the unsigned copy.
3. `signpath/github-action-submit-signing-request` (pinned by commit) submits it and waits up to an hour for the manual approval.
4. The returned file must carry a `Valid` Authenticode signature; it then replaces the unsigned installer.
5. The regular `switchboard-windows-<arch>` artifact is uploaded, and `collect` computes `SHA256SUMS.txt` from the signed bytes. The in-app updater verifies against that manifest, so it accepts the signed installer.

Manual dispatches and branch builds are never signed, so they never wait on an approval.

Only the outer NSIS setup program is signed. That is the file Windows SmartScreen checks on download; the application and sidecar binaries inside it are not individually signed.

## One-time setup

1. Apply at <https://signpath.org/apply> with this repository. Requirements include an OSI license (MIT), a public repository, a "Code signing policy" section in the README (already present) and MFA on the GitHub and SignPath accounts of everyone in it.
2. In SignPath, under **Trusted build systems**, link the predefined **GitHub.com** system to the project.
3. Create the project with the slug you will put in `SIGNPATH_PROJECT_SLUG`, and a release signing policy (for example `release-signing`) that requires manual approval.
4. Set the project's artifact configuration to:

   ```xml
   <?xml version="1.0" encoding="utf-8"?>
   <artifact-configuration xmlns="http://signpath.io/artifact-configuration/v1">
     <parameters>
       <parameter name="version" required="true" />
     </parameters>
     <zip-file>
       <pe-file path="Luxury-Switchboard-${version}-windows-*-setup.exe" product-name="Luxury Switchboard" product-version="${version}">
         <authenticode-sign />
       </pe-file>
     </zip-file>
   </artifact-configuration>
   ```

5. Create a CI user in SignPath, give it submitter rights on the signing policy and generate its API token.
6. In GitHub, **Settings → Secrets and variables → Actions**:
   - secret `SIGNPATH_API_TOKEN`: the CI user's token. Never commit it or print it.
   - variables `SIGNPATH_ORGANIZATION_ID`, `SIGNPATH_PROJECT_SLUG`, `SIGNPATH_SIGNING_POLICY_SLUG`.

Setting `SIGNPATH_ORGANIZATION_ID` is what turns signing on; delete it to turn signing off again.

## Each release

Push the tag as usual. Both Windows jobs pause at **Sign the installer with SignPath**; approve the two signing requests in SignPath within an hour. A rejected or expired request fails the Windows job, and the release is not collected, rather than shipping an unsigned file under a signed release.

## Alternative

Azure Artifact Signing (formerly Trusted Signing) is a paid alternative with identity validation that is only available to organizations and individuals in a limited set of countries. It is not wired into this workflow.
