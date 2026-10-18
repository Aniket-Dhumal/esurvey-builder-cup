# eSurvey — farmer app

The Flutter app from `docs/DEVELOPMENT_PLAN.md` M5. A farmer photographs their
own crop damage, standing in the field, usually with no signal.

## Why native rather than the browser

`README.md` at the repo root argues for a PWA, and that argument is good: a
farmer files one claim a year, and asking them to install an app is where most
of them stop. The PWA in `web-platform/` remains, and remains the fallback for
anybody who will not install anything.

What a native app buys, and why it was chosen here:

- **The keystore.** A refresh token behind the Android keystore can be
  long-lived enough that a farmer who files once a year never signs in twice.
  A browser cannot hold a credential that way, so the PWA has to ask for an
  OTP far more often — over the network they do not have.
- **Frame-synced sensor telemetry.** IMU, GNSS and barometer readings tied to
  the camera frame, which the browser cannot give at all. Not yet built; see
  below.
- **Background upload.** A queue that drains when signal returns without the
  app being open, which a service worker on a low-end Android does
  unreliably at best. This is the one that matters most: a farmer files once
  a year, so "it uploads next time they open the app" means it may never
  upload at all.

## What is here

**Core** — `lib/core/`

| File | What it is for |
|---|---|
| `db.dart` | The offline store. SQLite, and the system of record for as long as the farmer is offline: whatever is on this phone *is* the claim until it reaches the office. |
| `session.dart` | A session that survives no signal. An expired access token is **not** a logout; only a missing or refused refresh token is. |
| `api.dart` | The HTTP client. Refresh is single-flight, because the server treats a replayed refresh token as theft and would end the session. |
| `sync.dart` | The queue. Boundaries, then reports, then photographs — an order that is a correctness requirement, not a preference. |
| `background.dart` | The same queue, draining with the app closed. Runs in its own isolate, over the same database. |
| `voice.dart` | Marathi speech, falling back to Hindi then English then silence, with text on screen regardless. The on/off preference lives here and survives a restart. |
| `storage.dart` | Whether there is room for another photograph, and sweeping up files no row points at. |
| `sensors.dart` | Position with its accuracy always visible, and camera tilt from the accelerometer. |
| `farmer_data.dart` | Cache-first reads. The phone answers first; the network only ever replaces. |

**Screens** — `lib/`

`onboarding/` a spoken three-page walkthrough · `auth/` mobile + OTP sign-in ·
`home/` the farmer's own screen · `report/` filing a report ·
`capture/` the voice-guided photograph sequence · `boundary/` the plot walk ·
`help/` the questions people go to the office to ask ·
`ui/theme.dart` the design system.

## The guided flow

Every screen a farmer has to act on speaks, and the speech is driven by where
they actually are rather than by a script read at the door.

| Screen | What it says, and when |
|---|---|
| Onboarding | One line per page, spoken as the page appears. |
| Sign in | The step you are on. Again when the step changes, because the two look alike and take different numbers. |
| Report | Each field as it is focused. The estimate caveat the first time the slider moves. The declaration when the fields are done. |
| Boundary | How to start, then what a corner is for once the first one is recorded. |
| Capture | The stage instruction, the tilt nudges, and confirmation that the photograph saved. |
| Help | The answer to whichever question was tapped. |

Three things about this are deliberate.

**Instructions are spoken one at a time, at the moment they apply.** The
report screen has four fields and a slider. Saying all of it when the screen
opens is not guidance, it is a paragraph read aloud — which is what the screen
already was for somebody who reads slowly. Each field speaks when it is
focused, and nothing speaks twice.

**"Say that again" is on every screen where an instruction can be missed** —
sign in, the report form, the boundary walk, and the capture sequence.
`Voice.say` refuses to repeat its own last line, which is correct while a
sequence is running (a rebuild would otherwise say the same instruction
twice) and wrong the moment somebody asks. A tractor going past costs a
farmer the instruction, and without this there is no way to ask for it back.

Onboarding and the help screen do without it: onboarding is one line per page
and the page can be gone back to, and the help screen re-speaks an answer
whenever its question is tapped, so both already answer the same need.

**The voice has a switch, and the switch is remembered.** Without one, a
farmer who does not want the phone talking turns the handset's volume down
instead — and then misses the capture instructions, which are the ones that
decide whether a photograph is usable at all. The control is hidden rather
than greyed on a phone with no speech engine: there is nothing to turn off,
and a dead control invites somebody to press it until they conclude the app is
broken. Turning it *on* speaks a confirmation, because that is the only way to
tell it worked; turning it off needs none, as the silence is the confirmation.

## Running out of room

The handsets this runs on ship with 8 or 16 GB and are full of other people's
forwards, so a full phone is an ordinary condition rather than an edge case.
`storage.dart` covers it in two places.

**Before a capture sequence starts,** while there is still time to delete
something. It writes two megabytes to the exact directory the photographs go
in and deletes it again, rather than asking a plugin how much space is free —
free-space APIs answer a slightly different question, because a filesystem can
report space that a quota or a read-only remount will not actually hand over.
Testing the operation itself cannot be wrong about its own result. Failing the
check does not block the capture; it shows a banner and says so aloud, because
the farmer is standing in the field either way and a smaller number of
photographs beats none.

**After a write that failed anyway.** A photograph is a file and then a row,
and those are two operations. If the row fails — most likely because the disk
filled between them — the file is left referenced by nothing. It can never be
uploaded, because the queue is built from rows, and never deleted, because
deletion happens when a row is sent. So it accumulates, silently, on the phone
least able to afford it, in the failure mode that was already about running
out of space. The capture path removes it immediately, and a sweep at startup
catches whatever a crash left behind.

The sweep reads rows first and the directory second. The other order would
race a capture in flight: a file listed before its row was written looks
exactly like an orphan, and would be deleted out from under the farmer who had
just taken it.

Signing out uses the same sweep. `clearPersonalData` deletes rows and knows
nothing about where photographs live — but with every row gone, every file is
an orphan, so one call removes them all. That is deliberate reuse rather than
a second deletion path to keep in step with the first, and it runs at sign-out
rather than waiting for the next startup, because on the borrowed handset this
is for, the next person may pick the phone up before then.

## Two ways work disappears quietly

A queue that stops draining is visible. A queue that stops *counting* looks
exactly like a queue that finished, and both of the faults below were that
second kind.

**A report that gave up.** After `maxAttempts` a row is left alone, which is
right — no number of retries changes a server's mind, and a phone should not
burn its battery finding that out. But `pendingDrafts` and its siblings filter
on `attempts < maxAttempts`, so the moment a row gives up it leaves every
count the home screen reads. The queue card disappears. The farmer is shown
nothing outstanding and concludes the report went. It did not, and nothing
would ever try again or say so.

`Store.stuck()` finds those rows and the home screen shows them in the loudest
card it has, with a retry. The attempt budget is a limit on effort, not a
verdict on the evidence, and a farmer who has just walked back into signal
knows something the budget did not.

**A capture that was interrupted.** A flat battery, a dropped phone, a back
press at the wrong moment. The capture sequence is only ever reached by going
forward from the report form, so there was no route back into a half-finished
one: the report would sync with fewer photographs than it needs, which an
officer reads as a weak claim rather than an interrupted one, and the walk to
the field is lost.

`Store.draftsWithPhotographs()` reports every local draft with its photograph
count, and anything short of `requiredPhotographs` gets a card that reopens
the sequence at `Progress.firstUnsatisfied` — the first stage still missing a
frame, not the beginning. Walking a farmer back through photographs already on
the phone reads as the app having lost them, and this screen is reached
precisely because something interrupted them once already. The count comes from the stage definitions
rather than being written down separately, so adding a stage cannot leave the
home screen believing an unfinished report is finished.

The screen filters to drafts with no `server_id`: adding photographs to a
report the office already holds is not something this app can do, so it does
not offer it.

## Sending with the app closed

A farmer walks the boundary, files, and photographs the damage standing in a
field with no signal. Then they pocket the phone and go home. With only a
foreground timer nothing happens next — they file once a year and have no
reason to reopen the app, so the evidence sits on the handset until the phone
is lost or the claim window closes. As far as the farmer is concerned the
report was filed; the office never received it.

`background.dart` registers two schedules with Android's WorkManager, both
constrained to `NetworkType.connected` so a phone that spends the day out of
signal is never woken for them:

- a **periodic** pass every fifteen minutes (Android's floor, not a choice),
  the standing safety net;
- a **one-off** pass requested whenever a sync ends with the network down and
  work still queued — so the first moment of signal on the walk home is when
  the evidence leaves, rather than up to fifteen minutes after it.

Three things about this are worth knowing before changing any of it.

**It runs in a separate isolate.** No access to anything the app holds in
memory. That is why `Sync` takes its store and api as arguments rather than
reaching for globals: the background isolate builds its own of each, over the
same database file and the same keystore, and the sync engine cannot tell the
difference.

**Both isolates open the same database, so there is a lease.** `Sync._running`
is a field, and a field is invisible across an isolate boundary — with only
that guard, a farmer who has the app open when the worker fires gets two
passes over one queue, and the second sends every photograph the first is
still uploading. The office receives each piece of evidence twice, from the
same farmer, with the same timestamp, which is what a fraud check exists to
notice. `Store.claimSyncLease` puts the lock in the one place both isolates
can see, as a single atomic statement, with an expiry — because Android kills
background isolates without warning, and a lease with no expiry would let one
such death stop the phone syncing permanently.

**The handler's return value is the whole contract with Android.** `true`
means done, `false` means retry with backoff. Only "offline" returns `false`:
a refusal the server will repeat is already recorded against the row it
belongs to, and retrying it would burn the phone's battery to be told the
same thing.

Failing to schedule any of this is not fatal, and deliberately so. The
foreground timer still drains the queue while the app is open — strictly
worse, and still working. A farmer should not be held at the splash screen
because a scheduling API said no.

## Answering the farmer

`help/questions.dart` holds the questions asked at a taluka office counter --
what happens next, when will somebody come, how much will I get -- with the
honest answer to each, spoken in Marathi and shown in both languages.

The answers are kept as data rather than built into the screen so they can be
asserted against, and the assertions are the point. No answer may name an
amount, and none may promise a date. Both are things this platform cannot
deliver and a farmer who has just lost a crop would plan around: "an officer
will come within seven days" is a sentence nobody in this system can keep,
and the farmer who waits eight days believes they have been forgotten.

The money question is answered rather than avoided, because it is the first
thing anybody wants to know and this app is certain of the answer: it does
not decide any amount and it does not pay. Compensation is decided under
government rules the app has no part in.

The screen ends by saying what it cannot do — if the question is not here,
ask at the taluka office. An app that answers only the questions it finds
convenient sends the farmer to the counter anyway, which is the queue this
platform exists to shorten.

## What is not here

**Frame-synced IMU telemetry.** The development plan warns that Flutter carries
the UI and the offline store well and does not carry the sensor pipeline, and
that is right: high-rate IMU tied to camera frame timestamps needs a platform
channel into native Kotlin. What exists today is per-capture position and a
smoothed tilt, which is enough to guide a farmer and not enough to be the
attestation the plan describes.

It is also the one thing here that cannot be written honestly without a
handset to run it on. Every other claim in this file was verified against a
build or a test; native sensor timing can only be verified on hardware, and
code nobody has run is not a feature.

**Push notifications.** Deliberately not built. FCM would mean a Google
dependency and a `google-services.json` in the build, and the platform already
has an SMS gateway — SMS reaches a farmer whether or not the app is installed,
open, or has been granted notification permission, which push does none of.
In-app, the sync engine refreshes the report list whenever it runs, so a farmer
opening the app sees the current state. That is polling-on-open, and it is
honest about being that.

## Running it

```
flutter pub get
flutter test
flutter run --dart-define=ESURVEY_API=http://10.0.2.2:8090
```

The default API base is the Android emulator's route to the host, which is
unreachable from a real handset — so a release build that forgot the define
fails loudly on the first request rather than quietly talking to nothing.

## What to ship

### What this tree currently produces is debug-signed

`android/key.properties` does not exist here, so `flutter build apk --release`
falls back to the debug keystore and `apksigner verify --print-certs` reports
`CN=Android Debug`. The build succeeds and the APK installs, which is exactly
what makes it easy to hand to somebody by mistake.

That is a deliberate hold, not an oversight — the instruction was to leave it
debug-signed for now. What it costs, and how to undo it, is under
[Signing a release](#signing-a-release) below. The short version: it cannot be
distributed, and any handset that receives one has to be wiped before it can
take a properly signed build.

There is also no release channel, no update mechanism, and no version pinning
between the app and the API. A handset that has not been updated in a season
talks to today's API and finds out at the first request whether that still
works.

### One APK per processor

Build one APK per processor, not the fat one:

```
flutter build apk --release --split-per-abi --dart-define=ESURVEY_API=https://...
```

Measured on this tree:

| Build | Size |
|---|---|
| debug | 155 MB |
| release, all ABIs in one APK | 54 MB |
| release, `armeabi-v7a` only | 17 MB |
| release, `arm64-v8a` only | 19 MB |

The fat APK carries native code for three processors and every handset throws
two of them away after downloading them. That is 35 MB of a farmer's data,
over a connection that is the reason this app works offline in the first
place, spent on nothing. Almost every handset in the field is `arm64-v8a`;
`armeabi-v7a` covers the older ones, and `x86_64` is emulators only.

If the app ever goes to the Play Store, `flutter build appbundle` makes this
automatic and is the better answer. Until then it is a manual split, and the
two ARM files are the ones to hand out.

## Signing a release

The release build is signed with a real upload key when `android/key.properties`
exists, and falls back to debug keys with a warning when it does not — so a
fresh clone still builds and `flutter run --release` still works, and nobody
has to hold a credential to test the app.

Generating the key is yours to do, not the build's, and it happens once:

```
keytool -genkey -v -keystore ~/esurvey-upload.jks   -keyalg RSA -keysize 2048 -validity 10000 -alias upload
```

Then write `android/key.properties`, which `.gitignore` already refuses:

```
storeFile=/absolute/path/to/esurvey-upload.jks
storePassword=...
keyAlias=upload
keyPassword=...
```

Two things worth knowing before that key is used for anything real:

- **It is not recoverable.** The signature is the app's identity to Android:
  losing this key means no existing installation can ever be upgraded in
  place, only uninstalled and reinstalled — and a farmer whose queued reports
  live in that app's storage loses them when it goes. Back the file up
  somewhere that is not this machine.
- **A debug-signed APK is not distributable.** It installs and it runs, which
  is exactly what makes it easy to hand to somebody by mistake. It can never
  be upgraded to a properly signed build, so a pilot handset that got one has
  to be wiped before it can get a real one.

## Build notes

Two things about this environment rather than the app:

- `compileSdk` is pinned to 36 in `android/build.gradle.kts` for every
  subproject. The local SDK installs API 37 as `android-37.0` and Gradle
  matches the directory name literally, so a build following
  `flutter.compileSdkVersion` looks for `android-37` and fails on a platform
  that is present under a name it does not recognise.

  The release build therefore prints a warning that `flutter_secure_storage`
  wants to be compiled against SDK 37, and suggests raising `compileSdk` to
  match. Do not take that suggestion on this machine: it is the exact change
  that fails, for the reason above. The warning is correct in general and
  wrong here, and the build succeeds regardless — the plugin compiles fine
  against 36. Once the SDK installs API 37 under a directory Gradle
  recognises, raise both pins together.
- `minSdk` is 23, not the Flutter default of 24. That is the floor for
  `flutter_secure_storage` v11's keystore-backed storage, below which a
  refresh token has nowhere safe to live — and Android 6 handsets are still in
  use in rural Maharashtra.
