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
- **Transparent re-login** — the `memd_session` cookie expires roughly daily;
  the app re-authenticates with your stored credentials and retries once,
  automatically. Credentials and the cookie live in
  EncryptedSharedPreferences; everything else in DataStore.

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
