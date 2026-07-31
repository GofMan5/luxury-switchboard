# Switchboard setup

1. Install `Switchboard` with the included NSIS installer.
2. Set your own Lite key once if needed:

   ```powershell
   [Environment]::SetEnvironmentVariable('FREEMODEL_API_KEY', 'your-key', 'User')
   ```

3. Add personal/shared provider keys in **API Keys**. Secrets are encrypted for the current Windows user and cannot be transferred from another account.
4. For public publishing, obtain your own publisher profile plus the matching `model-tunnel_ed25519` and `model-tunnel_control_ed25519` identities from the administrator. Place them in:

   ```text
   %LOCALAPPDATA%\ProviderSwitchboard\ssh\
   ```

5. Paste only the issued `v1.<port>.<slug>` profile into **Tunnel**. Do not send SSH identities, API keys, DPAPI files or tunnel access keys inside this archive.

Each owner receives a distinct reverse port, slug and SSH identity, so two installed relays do not conflict. **Shared Control** shows the local owner's row as **Your connection** and other owners as neutral tunnel numbers.

This private build is not Authenticode-signed, so Windows may show a SmartScreen warning. Verify the installer against `SHA256SUMS.txt` before running it.
