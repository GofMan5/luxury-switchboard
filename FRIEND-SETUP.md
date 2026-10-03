# Luxury Switchboard setup

1. Install `Luxury Switchboard` with the included NSIS installer.
2. If you already had a build named plain `Switchboard`, remove it from **Settings → Apps** after this install finishes. The rename moved the install directory and the uninstall entry, so the old copy stays behind as a second application: same encrypted configuration, same relay port, two icons. Uninstalling it keeps your providers, keys and routes — they live outside the install directory, in `%LOCALAPPDATA%\ProviderSwitchboard`.
3. Add every provider key explicitly in **API Keys**. Secrets are encrypted for the current Windows user and cannot be transferred from another account.
4. Publishing is one click now: **Tunnel → Start** downloads the tunnel connector once (verified against its pinned checksum) and gives you an HTTPS address plus a stable access key. Share the current address and the key. The address is re-rolled every time the tunnel starts — the key is not.

This build is not Authenticode-signed, so Windows may show a SmartScreen warning. Verify the installer against `SHA256SUMS.txt` before running it.
