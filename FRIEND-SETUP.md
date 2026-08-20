# Luxury Switchboard setup

1. Install `Luxury Switchboard` with the included NSIS installer.
2. If you already had a build named plain `Switchboard`, remove it from **Settings → Apps** after this install finishes. The rename moved the install directory and the uninstall entry, so the old copy stays behind as a second application: same encrypted configuration, same relay port, two icons. Uninstalling it keeps your providers, keys and routes — they live outside the install directory, in `%LOCALAPPDATA%\ProviderSwitchboard`.
3. Add every personal/shared provider key explicitly in **API Keys**. Secrets are encrypted for the current Windows user and cannot be transferred from another account.
4. For public publishing, obtain your own publisher profile plus the matching `model-tunnel_ed25519` and `model-tunnel_control_ed25519` identities from the administrator. Place them in:

   ```text
   %LOCALAPPDATA%\ProviderSwitchboard\ssh\
   ```

5. Paste only the issued `v1.<port>.<slug>` profile into **Tunnel**. Do not send SSH identities, API keys, DPAPI files or tunnel access keys inside this archive.

Each owner receives a distinct reverse port, slug and SSH identity, so two installed relays do not conflict. **Shared Control** shows the local owner's row as **Your connection** and other owners as neutral tunnel numbers.

This private build is not Authenticode-signed, so Windows may show a SmartScreen warning. Verify the installer against `SHA256SUMS.txt` before running it.
