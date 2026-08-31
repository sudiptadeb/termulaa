# Termulaa for Android

A phone client for a memd server's termulaa reverse-tunnel rendezvous
(default: `https://memd.debkosh.com`). You run long-lived agents in terminal
sessions on remote machines; the phone is for checking on them, getting
notified when a session produces output or a machine drops offline, and
opening the full termulaa web terminal when you need to interact.

*(Screenshots later — the app is a dark terminal-styled three-screen UI:
Connect → Machines → Terminal.)*

## What it does

- **Machines** — live list from `/rc/api/agents`, merged with a locally
  remembered machine table so machines that drop off the server show as
  *offline* instead of vanishing. Amber *unseen* pill when a session produced
  output you haven't looked at.
- **Notifications** — a periodic background check (15/30/60 min, WorkManager)
  and an optional 45-second live-watch foreground service. One notification
  per machine for new session output; separate channel for offline alerts.
  Tapping one deep-links straight into that machine's terminal.
- **Terminal** — the full termulaa web UI (tabs, split panes, key bar) in a
  locked-down WebView sharing the memd session cookie. The app adds nothing
  inside the page.
- **Pairing-code sign-in** — the primary way in. Open the termulaa section of
  your memd dashboard, tap *Pair the app*, and type (or tap) the short code
  into the Connect screen. This works for **every** account type, including
  Google/SSO sign-in, because the code is minted by your already-signed-in
  dashboard. The code is single-use and expires after 5 minutes; case, dashes
  and spaces don't matter. The dashboard also renders the code as a
  `termulaa://pair?...` link — tapping it on the phone prefills and pairs in
  one step (if the link is for a *different* server than the one you're
  signed in to, the app prefills the form but waits for you to confirm the
  switch, since pairing replaces the old sign-in).
- **Transparent re-auth** — redeeming a code gives the app a long-lived,
  revocable app token. The `memd_session` cookie expires roughly daily; the
  app silently exchanges the token for a fresh cookie and retries once,
  automatically. The token and cookie live in EncryptedSharedPreferences;
  everything else in DataStore.
- **Revocation from the dashboard** — every paired phone appears in the
  dashboard's *Paired phones* list (label, paired date, last used) with a
  revoke button. Revoking un-pairs that phone remotely: its next re-auth
  fails and the app drops to the Connect screen with a plain "this phone was
  un-paired" notice. Signing out in the app also revokes its own token
  (best-effort), so the list stays honest.
- **Password sign-in** (secondary, collapsed behind *Sign in with password
  instead*) — for servers with local username/password accounts only; OIDC
  accounts have no password, use pairing. This is the old v1.0 behavior:
  stored credentials, automatic re-login on cookie expiry.

Plain-`http://` servers are unsupported in v1 — the URL field requires
`https`, except literal IPs / `localhost` for development (and the network
security config only actually permits cleartext to loopback and the emulator
host alias `10.0.2.2`).

## Build

Requirements: JDK 17+ and an Android SDK with platform 35.

```sh
cd android
ANDROID_HOME=/path/to/android-sdk ./gradlew :app:assembleRelease
# → app/build/outputs/apk/release/app-release.apk

# tests (JVM + Robolectric) and lint, run against the release variant:
ANDROID_HOME=... ./gradlew :app:testReleaseUnitTest :app:lintRelease
```

## Sideload keystore disclaimer

`signing/sideload.jks` is **committed to this repository on purpose**, along
with its passwords in `app/build.gradle.kts`. It exists solely so that every
build — yours, mine, CI's — signs with the same key and Android will install
one build **over** another without an uninstall (install continuity).

It provides **zero authenticity**: anyone can build and sign an APK with this
public key. Do not trust an APK because it carries this signature; trust only
APKs you built yourself or obtained from a source you trust. If you fork this
project for real distribution, generate your own private keystore.
