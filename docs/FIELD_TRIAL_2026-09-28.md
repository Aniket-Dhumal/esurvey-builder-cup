# Field trial, 28 September 2026 — what broke

One drought claim on survey 453 (Malegaon, Barshi), walked by a farmer on their
own handset against the staging deployment at `esurvey.mindstrix.com`.

The claim completed: four clips, 100 MB, all four carrying their station number,
`reported_channel = farmer_app`, no invented signature, accuracies ±3.4–4.9 m.
It took roughly three hours, of which about one was lost to a single defect
nobody could see.

What follows is everything observed, whether or not it was fixed on the day.
Fixed items are recorded too: several were silent failures that looked like
success, and the next person to touch them should know they have a history.

---

## Fixed on 29 September — app release 1.2.0+3

Items 1 to 4 below are now addressed. Their entries are kept in full because
each was a silent failure and the next person to touch them should know they
have a history; what changed is recorded under each.

**Evidence is now 17.9 MB a claim, from 100 MB.** 720p at 15 fps and 2.5 Mbit/s:
the office reads about a dozen still keyframes from each clip and never watches
the footage, so halving the frame rate doubles the bits in every frame the
assessment actually looks at. Same file size as 30 fps would give, twice the
quality where it counts.

The settings are **negotiated, not set**. An Android encoder may refuse a frame
rate outright, or accept a bitrate and ignore it, and a farmer in a field whose
phone will not record has lost the claim — which is a far worse outcome than a
large upload. So there is a ladder: 15 fps, then 20, then 24, then bitrate-only,
then whatever the phone does. Every rung but the last fits the 20 MB budget, and
the profile used is stored with each capture so a 100 MB claim can be read as a
fact about the handset rather than guessed at. A clip over 8 MB steps the rung
down and reopens the camera for the remaining stations.

*A flaw caught in review:* the first version raised the bitrate as the frame rate
rose, to hold per-frame quality constant. That put the fallback rungs at 21, 25
and 29 MB a claim — so a phone refusing 15 fps would quietly break the budget the
ladder exists to meet. The budget is the constraint; per-frame quality is what
gives way.

**Chunks are 512 KB, from 2 MB** — three to five seconds of exposure at the
throughput actually observed, rather than ten to twenty. The server's ceiling is
2 MB and it honours a smaller request, so nothing changed there.

**A foreground service keeps an upload alive across a locked screen**, with a
notification for as long as it runs. It executes one pass and stops itself, so
the notification cannot outlive the work, and it takes the same device-wide
SQLite lease every other pass takes — a third isolate is no more dangerous than
the second.

**The orphaned-evidence message was false.** It said "this phone is still sending
another farmer's claim" whenever another account held rows, which is true only
while that account's parked credentials are usable. It now distinguishes the two
and, in the stranded case, names the number that can recover the work — masked to
four digits, because a field phone gets handed around.

---

## Found while fixing the above

### 23. An unguarded migration could have bricked the app's database

The new `ALTER TABLE captures ADD COLUMN encoding_profile` was written without a
column check. A migration that fails part-way leaves the schema version
unchanged, so the next open runs it again — and the second attempt throws
`duplicate column name` every time from then on. The farmer's app would never
open its own database again, with a full queue inside it.

Now idempotent, matching the neighbouring migrations, which already guard this
way. **Every future migration must.**

### 24. The APK ships RECORD_AUDIO, and the manifest says it does not

Our manifest carried the comment "No RECORD_AUDIO. The camera runs with
enableAudio: false, so the farmer would be granting microphone access for
nothing." The merged manifest of the built APK contains it: `camera_android_camerax`
declares it unconditionally and manifest merging brings it in. Nothing in this
app records audio — `flutter_tts` is output only.

So a farmer installing this is asked for microphone access the app never uses, on
a government application handling identified people's land records.

It can be stripped with `tools:node="remove"`, and has **not** been: with audio
disabled CameraX does not need the permission, but "does not need" is a judgement
about every Android build in Maharashtra, and being wrong means a farmer whose
phone will not record. Worth removing once it can be tried on real handsets. The
false comment has been corrected either way — a manifest that asserts the
opposite of what it ships is worse than the permission.

Also arriving by merge and unused: `RECEIVE_BOOT_COMPLETED` and `WAKE_LOCK` from
the foreground-task plugin (`autoRunOnBoot` is false), `FOREGROUND_SERVICE_SHORT_SERVICE`,
and the legacy storage pair.

### 26. Signing out of a finished claim's evidence took one tap, with no warning

The farmer picker carries a logout icon wired straight to `onSignOut` — no
dialog, no confirmation. A farmer who had filed a claim and whose four clips were
still queued could strand all of it with one tap, and nothing would have said so.

The home screen's own sign-out dialog did exist, and what it said was wrong:
waiting work "keeps sending from this phone after you sign out, and is removed
once it arrives". That holds only while the signed-out account's parked
credentials survive. On the trial a session lapsed, the parked token died with
it, and two and a half recordings sat on the phone for an hour — with that dialog
having promised they were on their way.

Now the guard is **in `_signOut()` itself**, not in a dialog, because there was
more than one way out and a check in one screen would have left the other open —
which is the same shape of bug. Sending is offered first and is the default:

> *4 recording(s) from your completed claim have not reached the office. They are
> safe on this phone, but they are only sent while you are signed in with this
> number. If you sign out they wait here, and will not reach the office until you
> sign in again with this same number.*
>
> `[ Sign out anyway ]` `[ Wait and send ]`

Not dismissible by tapping outside: that should not be the way past a dialog
about losing a claim.

**It is overridable, deliberately.** A refusal with no way past it would make a
handset in a village with no signal unusable by anybody until signal returned,
which can be days — and this app has a great deal of borrowed-handset machinery
that assumes otherwise. What changed is that stranding evidence is now a
deliberate choice, described by what it costs.

**Only a finished claim holds it.** A draft still being gathered (`status =
'holding'`) is work the farmer may be abandoning, and it does not block. Nor does
another account's work, which the signing-out farmer could not send however long
they waited.

*Caught in review:* the first version of `unsentFiledEvidence` wrote its own SQL,
which turned out to be byte-for-byte the predicate `_outstandingWhere` already
uses for captures. Two copies of a rule about not losing a farmer's claim is one
too many; it now delegates, and a test asserts the two agree.

### 27. Permissions were discovered in the field, and a refusal read as a broken phone

Every permission was asked for at the moment it was first needed. That put the
camera prompt on the walk screen and the location prompt a second later — so a
farmer who declined either, or whose phone had location switched off, found out
standing in their field. The remedy is Settings, and often a signal, and they had
neither to hand.

A refused camera did not even read as a refusal. The new recording-profile ladder
tries five encoder settings, and **every one of them fails when the permission is
missing**, so the farmer was told "the camera would not start at any recording
quality" — which sounds like a broken handset and sends them looking for another
phone instead of into Settings. Fixed: a `CameraAccessDenied` now stops the ladder
at the first rung and is reported as what it is.

`AccessGate` now sits in front of the walk, outside `FieldGate` — that order is
forced, because FieldGate cannot know where the farmer is without the location
permission.

**On asking early.** The usual advice is never to ask at launch, and this app
already followed it deliberately: the notification request is deferred with the
comment "a farmer who is asked before they have done anything has no way to judge
the request and will refuse it." That reasoning is right, and it is about a *cold*
prompt. So the first walk on a phone shows a screen that says, in Marathi first,
what each permission is for and what the app cannot do without it; the system
prompts follow the farmer's decision to continue. After that the gate is silent —
asking for a permission already held opens no prompt, so a farmer who has granted
everything never sees the screen again.

**Required, and not:**

| | |
|---|---|
| camera | required — nothing to record without it |
| precise location | required — an approximate fix is hundreds of metres wide, so a recording cannot be placed inside the farmer's own boundary |
| location switched on | required, and *separate* — a granted permission over a disabled radio yields nothing, and it is a different Settings page |
| notifications | **not** required — the upload runs and the claim completes without them |

An approximate location is the case a granted permission hides: the walk would
run, every fix would fail the geofence, and the farmer would be told they were
outside their own field. It is detected (`Geolocator.getLocationAccuracy`) and
reported separately, because the remedy differs — asking again changes nothing,
only Settings can make it precise.

**The microphone is not asked for, and the screen says so.** `enableAudio: false`,
and the camera plugin checks the audio permission only when audio is enabled, so
nothing in this app records sound. Asking a farmer for microphone access on a
government application handling their land records, for a feature that does not
exist, would be an overreach. The screen carries a line stating the app does not
record sound — a government application that says plainly what it does *not* take
is easier to trust than one that merely takes less. A test fails if the mic is
ever added to the report without a deliberate decision.

No new dependency: `geolocator` already opens both Settings pages, the camera
plugin's own `initialize()` is what raises the prompt, and
`flutter_local_notifications` can report and request its own.

### 28. The app had no error handler of any kind

No `FlutterError.onError`, no `PlatformDispatcher.instance.onError`, no guarded
zone. It also has ninety-odd `unawaited(...)` calls — which is the right pattern
for fire-and-forget work, and means ninety-odd places where a throw goes nowhere.

Those two facts combine badly in a farmer's hands. An error in some unrelated
future — a voice engine that is not installed, a notification channel that will
not open — either killed the app or vanished. **Killing it is the worse one:** a
farmer three points into a walk loses the walk, and the only record of why is a
phone with no signal in a village.

`Faults` now installs both handlers before `runApp`. An unhandled async error is
reported as handled, so it cannot take down a walk in progress — and every one is
written to a bounded table on the phone, where it survives a restart.

**Why stored and not sent.** The trial was diagnosed through `adb logcat` over a
cable, which needs the handset in your hand and the farmer's day. A stored fault
needs only the phone to come back. Nothing is transmitted: a crash reporter on a
government application handling identified farmers' land records would be a
data-sharing decision, not a robustness one.

Readable from Help → Technical faults, which says plainly that the farmer has done
nothing wrong and their claim is unaffected. Copy-to-clipboard, no upload. Cleared
on sign-out once nothing is left to send.

**The caught errors mattered as much as the uncaught ones.** The app catches
carefully in many places and then `debugPrint`s, which reaches a cable and nobody
else. The ones on the evidence path now also write a fault: a sync pass that
failed, a background retry that could not be scheduled, and the three that stop a
farmer recording — camera, record, save. `walk.save` is the worst of them: the
recording happened and did not survive.

**Checked and deliberately left alone**, so the next person does not redo it:

- `setState` after `await` — scanned the whole of `lib/`; every candidate already
  has its `mounted` guard, including the one in `file_report_screen` that looks
  unguarded until you see the `return` in the branch above it.
- Startup — opens the store and the keystore and touches no network, exactly as
  its comment promises. Nothing to gain.
- `listSync`/`deleteSync` in the orphan sweep — runs mostly on the background
  isolate, and at four files per claim it is sub-millisecond on the UI one.
  Changing it would be churn.

### 29. A refusal of the phone was treated as a verdict on the evidence

`classify` sent everything it did not recognise to `Verdict.permanent`, which
writes `status = 'failed'` — and `pendingCaptures` excludes `failed`. Terminal.
Nothing ever retries it.

**401 and 403 were in that bucket.** A 401 is a token the server would not
accept; a 403 is a grant it did not find. Neither says anything about the clip:
the same bytes are acceptable the moment somebody signs in or the office issues
the grant. Treated as permanent, they marked a farmer's recording failed for
good, and the farmer would have been told their claim was complete.

It was not reachable — and only just. A 401 is caught a layer up, where a failed
refresh is reported as *offline*; offline stops the pass; and the next pass checks
`signedIn` before draining. **Three separate facts holding at once**, any one of
which changing would reopen a silent data-loss path. And the trial had a grant bug
producing exactly these codes on a neighbouring route, so a capture that met it
would have been abandoned.

Now `Verdict.unauthorized`: the item is left completely untouched — not sent, not
failed, not even charged a retry, because it did nothing wrong — and the pass
stops, since every remaining item would meet the same refusal. A fifteen-minute
floor under the next wake: the usual remedy is the farmer signing in, which runs a
pass of its own, so the floor is for the other case — a grant the office has yet
to issue, which resolves without the farmer and should not be asked about every
thirty seconds until it does.

### 30. Changing the chunk size could have cost a farmer their whole clip

The server assembles an upload by concatenating the parts it was given, in order,
so the slices of one upload must agree with each other — and `next_chunk` counts
them at whatever size the upload *began* with.

This release took the chunk size from 2 MB to 512 KB. A farmer who upgraded
part-way through an upload would resume one the server was counting in 2 MB
chunks, while the client computed offsets from its own new constant: reading at
1.5 MB where the server already held 6. The assembled file would be wrong.

It self-heals — the server's checksum catches the mismatch, resets the upload and
the second attempt is consistent — so nothing is lost. What it costs is **the
whole clip again over a village connection**, which is the exact trade this
release exists to avoid.

The server already returns `chunk_size` in its begin response, and the client now
honours it. Correct by construction rather than self-healing by luck.

*Found in the same change:* the `chunkServer` test mock hardcoded a 2 MB
`chunk_size` default, which matched the client's constant when it was written and
stopped matching when the constant moved. A real server echoes the size the client
asked for. **Third time in this session** that a test encoded a constant instead of
the behaviour.

### What was checked and found sound

Worth recording so nobody re-audits it:

- **Server-side integrity.** The assembled file is hashed as a stream against the
  SHA-256 the handset declared before a byte was sent, and the byte count is
  checked. A mismatch resets the upload rather than refusing it — up to
  `MaxUploadRestarts` — and the client already treats `UPLOAD_RESTARTED` as
  transient. End to end, a damaged chunk is recoverable.
- **The abandoned-upload sweep.** `expires_at` advances seven days on every chunk,
  so a clip trickling in over a week of patchy signal is not swept; a previous
  review (H2) had already fixed the fixed-expiry version of that bug. And if a
  sweep does take one, the client gets `UPLOAD_NOT_FOUND`, which classifies as
  transient and begins again under the same key.
- **Idempotency and concurrency.** Every write carries a key stable across
  attempts, and the device-wide sync lease is an atomic SQLite upsert, so no
  number of isolates can double-send.

### 25. Two uploader tests were testing a constant, not the behaviour

Both hardcoded 2 MB and broke the moment the chunk size was tuned. Rewritten in
terms of `chunkBytes`, so they assert the resume rather than the number.

---

## Still open — fix before more farmers use this

### 1. A lapsed session orphans recorded evidence

**Severity: highest. This is the one that cost an hour, and it can lose a
farmer's claim.**

The upload queue is scoped by account (`owner_id`). The farmer's session
lapsed mid-upload; they signed in again on a *different* number
(`+918380855428`, a new account created at 13:20:08); and the two and a half
clips already recorded became invisible. The app reported
`pending_captures = 0` — truthfully, for that account — and uploaded nothing
for an hour while appearing healthy and online.

It recovered only when they signed back in as the original number.

Two separate faults:

- Evidence already recorded should belong to the farmer, not to whichever
  account happened to be signed in when it was captured. A re-authentication
  must not strand it.
- **Nothing said anything.** No "3 videos are waiting from a previous
  sign-in", no warning at sign-out, no error. The silence is what made this
  cost an hour instead of a minute — three of us concluded in turn that it was
  the network, then app suspension, then a scoping bug in the query.

At minimum the app must surface orphaned work and offer to reclaim it.

### 2. 25 MB for a fifteen-second clip

100 MB per claim over a village connection. This is the root cause of most of
the day's upload trouble: chunks that took 9–25 s, one that took 49.8 s before
dying, and a window long enough for the session above to lapse inside it.

Dropping the recording bitrate should cut this several-fold with no loss for
what the vision model reads — it scores a dozen keyframes, not the footage.

### 3. Uploads stop when the app is backgrounded

A 30-second timer drives the foreground pass. Android suspends it the moment
the phone is pocketed or the screen sleeps, and the WorkManager fallback wakes
roughly every fifteen minutes. A farmer who starts an upload and puts their
phone away has stopped it.

An upload this long needs a foreground service.

### 4. A dropped connection costs a whole chunk, and recovery is slow

Chunk size is 2 MB. The handset changed IP mid-upload twice (cell hopping),
killing the TCP connection and producing
`reading the chunk: unexpected EOF` → HTTP 500. That classifies as transient
and schedules a retry, which is right — but with 2 MB chunks each failure
re-sends a megabyte or more.

Smaller chunks would make a dropped minute cost seconds.

### 5. The app asks to "add farmer" when the farmer's own number signs in

`9527464552` is Harichndra's registered number and resolves to farmer
`56567763672` in the registry, yet after re-authenticating the app asked for a
farmer id and date of birth. A farmer signing in on their own registered
number should not have to identify themselves twice.

### 6. The staging server runs `ESURVEY_ENV=development`

Pre-existing, not introduced by this deployment. On an internet-facing host
holding identified farmers' land records and photographs this disables the
hardening `staging` enforces. TLS and a declared proxy are both now in place,
so it should start cleanly as `staging`.

### 7. `ground-vision/` is untracked by git

The whole service exists only on one developer's machine. It also has no CI
job, which follows from the same cause.

---

## Fixed on the day — recorded because each failed silently

### 8. `docker-compose.yml` did not pass seven SMS and OTP variables

`ESURVEY_SMS_OTP_TEXT` and `ESURVEY_SMS_PEID` were read by the configuration
and never passed into the container, so the service fell back to its built-in
Marathi default text and sent no DLT entity id. Every code was submitted
against an English-registered template, accepted by the aggregator with a
transaction id, and dropped by the operator. Five more
(`ESURVEY_SMS_WORKERS`, `ESURVEY_OTP_TTL`, `_LENGTH`, `_MAX_ATTEMPTS`,
`_RESEND_COOLDOWN`, `_DAILY_CAP`) were missing the same way but had sane
defaults, so anything tuned in `.env` was quietly ignored.

**A variable the configuration reads and the compose file does not pass is not
a default — it is a silent one.** Worth a startup check that every
`ESURVEY_*` the config reads is actually present.

### 9. The gateway needs `%20`, not `+`

`url.Values.Encode` writes a space as `+`, which is correct form-encoding and
which this gateway does not decode. The message text then failed to match the
DLT template. `curl --data-urlencode` writes `%20`, which is why the same
message sent by hand arrived and the same message sent by the platform did not.

### 10. The JSON endpoint accepts and never delivers

`POST /fe/api/v1/message` returns `SUBMIT_ACCEPTED` with a transaction id and
the message never arrives. `GET /fe/api/v1/send` delivers. The JSON endpoint
was chosen because it takes the credential in a header rather than a query
parameter, which is the better place for it; delivery won. The password now
reaches the provider's access log, so **rotate it on a schedule**.

### 11. SMS credentials were truncated in `.env`

Password was four characters where it is five; the DLT template and PEID were
each missing their leading `170`. Never noticed because one-time codes went to
the service log until the day of the trial, so the credentials had never once
been presented to the gateway.

### 12. `stations()` and `prefetch()` did not send the farmer grant

A handset acting for another farmer through a grant got
`404 — that plot is not on your record` on the sampling-point lookup. The form
swallowed the 404, so **"नोंद सुरू करा" did nothing at all** with no error
anywhere. Nothing server-side recorded a problem; the answer was only in
`adb logcat`.

Third grant-related bug of the day, after the same omission in `reports()` and
in the capture upload. They share a shape: a scoped query returning empty for
work that is plainly there, with no error. **Every `_read` caller deserves an
audit rather than one fix per field failure.**

### 13. The release APK was debug-signed

No `android/key.properties`, so the "release" build fell back to debug keys —
which install over adb and are blocked by Play Protect when a farmer taps the
file. A debug-signed install also cannot be upgraded in place by a properly
signed one, so any farmer who had installed it would have had to uninstall and
lose queued claims.

A 4096-bit release key now exists at `android/esurvey-release.jks`. **It is
gitignored and exists on one machine. Losing it means no installed app can ever
be updated.**

### 14. Two APKs with different signing keys were distributed

Debug-signed and release-signed builds of the same version were both sent out
within twenty minutes. Android refuses to install one over the other and says
only "App not installed". Also note split-per-ABI builds carry versionCode
2002 against the universal build's 2, so a phone that took the split build will
refuse the universal one as a downgrade.

### 15. The console was built with `localhost` as its API base

`NEXT_PUBLIC_API_URL` was unset at build time, so `API_BASE` fell back to
`http://localhost:8090`. The page loaded; every call from the browser went to
the user's own machine.

### 16. The deployment tarball omitted source

`--exclude='evidence'` was meant to skip the blob store and also removed
`backend/internal/evidence/`, so the API would not compile. `--exclude='out'`
removed the built console. `deploy/` was never listed, so Docker created
`pg_hba.conf` as a *directory* and Postgres refused to start.

### 17. A self-signed certificate blocked the app entirely

Caddy served `tls internal` on a bare IP. A browser can click past that; Dart's
`HttpClient` cannot, and Android blocks the cleartext fallback. There was no
URL that could be compiled into an APK until `esurvey.mindstrix.com` was
pointed at the VM and Caddy obtained a real certificate.

---

### 18. The deploy path reports success when it half-fails

`gcloud compute ssh --command "tar xzf … && docker compose build && up -d"`
exited 0 after SSH dropped part-way through the build. The source had been
extracted; the container was still the one from three hours earlier. Nothing
in the output said so.

Caught only by checking the running container's start time against the
expected one. Had it gone unnoticed, the next test would have run against the
old binary and shown the same symptom, sending the investigation somewhere
else entirely.

A deploy must verify what is *running*, not what was copied — compare a build
stamp or the container's start time, and fail loudly when they disagree.

---

## Fixed after the trial

### 19. No damage figure produced, despite four ground clips

> "Sentinel-2 at 10 m establishes that a crop grew and roughly when. It cannot
> measure how badly one was damaged, so a percentage only survives verification
> when a field photograph backed it. This assessment carries evidence for a
> field visit, not a payout."

A regression introduced by the video-only change made on the morning of the
trial, not a failure of the 27 September fix.

`verify.go` discards a damage figure produced without ground imagery, which is
correct and unchanged. `groundImagery()` deliberately skips video — a clip was
once sent as a photograph and unlocked a figure from footage the panel cannot
read. Photographs were therefore the only source of ground imagery, and
removing the still from the walk left the assessment with none at all.

Fixed by attaching one frame from each clip as `ImageSurveyKeyfr`, using the
extractor built the same day for the report's plates. The kind already existed
and already counted as ground imagery; nothing had produced one since the
still was removed. The clip itself is still never sent.

**The lesson worth keeping: removing an input broke a rule three files away,
and nothing failed — the assessment completed and simply declined to produce a
figure.** A walk that yields no readable image should say so as an absent
evidence source, not present as a considered refusal.

### 20. A declared drought came back as "contradicts" — on three defects at once

> "we need to fix your rainfall and weather source as drought is declared by
> govt and there is longer dry spell"

The district had declared it. Survey event `SE-2026-496-006`, "Drought Spell in
Solapur Barshi", `state = open`, declared 18 September by a named officer, over
an incident window of **1–20 September**. The claim names that event. All four
specialists reported the drought as contradicted by the record, and a
`calamity_not_in_record` fraud indicator was raised against the farmer.

Three independent faults, each sufficient on its own:

**a. The declaration was thrown away at the door.** `server.go` set
`CalamityDeclaredBy = "farmer"` for every claim unconditionally, and read the
survey event only in a branch gated on the calamity type being *absent* —
which for a claim it never is. So the district's declaration never reached the
panel for any claim ever filed.

The synthesis prompt already handled this correctly and had for some time:
"**Only where the farmer declared the calamity.** Where it came from the
district's survey event it is an official declaration, the farmer did not
assert it, and a fraud indicator would accuse them of somebody else's
statement." The rule was right, applicable, and starved of the one input it
tested. *A guardrail written against a field that is never populated is not a
guardrail.* It is now enforced in `guardrails.py` as well as asked for in the
prompt.

**b. The weather was read over the wrong fortnight.** `Assemble` always took
the fourteen days before the reported date. The farmer reported 1 September —
the first day of the declared drought — so the window ran **18 August to
1 September**: the two weeks immediately *preceding* the declared dry spell,
holding the last of the monsoon. The specialist was handed the wettest
fortnight available and asked whether drought was consistent with it.

A declared window now wins, and the season extends to its end so that
nineteen days of a declared drought cannot fall outside the season it is
judged in.

**c. The dry-spell threshold could not see a drought in a reanalysis.**
This is the one worth understanding, because the arithmetic was never wrong
and I told the user the thresholds were already correct before checking.

`rainDayMM` was 2.5 (IMD's rainy-day definition, correct) but `dryDayMM` was
**1.0**, on the reasoning that a millimetre should not count as a dry day.
That reasoning holds for a rain gauge and fails for ERA5, which resolves
convection by averaging it across an ~11 km cell: a downpour on one village is
returned as a thin film over all of them, and a day with no rain at all
acquires a millimetre or two.

Barshi's own numbers, from the archive:

| | |
|---|---|
| season rain days (≥2.5 mm) | 49 |
| season days holding only a trace (0.1–2.5 mm) | **52** |
| declared window, 1–20 Sep | 37.4 mm over 20 days, **3** rain days |
| longest dry spell in that window, at `dryDayMM = 1.0` | **4 days** |
| longest dry spell in that window, at 2.5 mm | **15 days**, from 5 Sep |

Seven days of the declared window held between 1.0 and 2.5 mm. Each of them
broke the spell. Twenty days of drought computed as four, and the note that
would have surfaced it required 21.

The drought was in the data the platform had already fetched, the whole time.

Fixed: `dryDayMM` is now `rainDayMM` — a day that was not a rain day is a dry
day, and the two figures can no longer contradict each other in the same table.
`longDrySpellDays` drops 21 → 14. Trace days are counted and reported, and both
the specialist prompt and the officer's report now state that a seasonal total
assembled largely from traces is **an upper bound on what reached the field**,
not a measurement of it — because "468 mm" is the most convincing number in that
section and was the least reliable one in it.

**What is still true and was not fixed:** ERA5 is a reanalysis, not an
observation. It remains the wrong instrument of record for a statutory drought
determination in India. What the platform can now do honestly is agree with the
declaration and say where its own instrument is weak.

### 21. The state publishes the figure the platform was missing

Maharashtra's own rainfall portal publishes, per taluka and per revenue circle,
rainfall recorded against the long-period normal for that unit. Barshi taluka,
kharif 2026, on the day of the trial:

| Period | Normal | Actual | % of normal |
|---|---|---|---|
| June | 107.3 | 81.6 | 76.0 |
| July | 123.7 | 134.6 | 108.8 |
| August | 124.5 | 19.4 | **15.6** |
| September | 161.1 | 50.4 | **31.3** |
| Season to 28 Sep | 516.6 | 286.0 | **55.4** |

ERA5 reported **505 mm** for the same season. The state's gauges say **286 mm**.
The reanalysis was reporting roughly double the rain that fell, and had no normal
to be compared against, so nothing in the platform could notice.

Now loaded by `esurvey-server rainfall-load <district>` into `state_rainfall`.

**Two limits found while building it, both worth knowing before relying on it:**

- **No daily series for the current year.** The portal serves circle-wise daily
  rainfall only for completed years; 2026 returns "Year should be LESS THAN
  2026". So a dry spell *inside this season* cannot be computed from this source.
  The split that follows is the honest one: amount and departure from normal come
  from the state, distribution across days from the reanalysis. Every figure
  should say which.
- **No village-to-circle mapping exists.** Villages hang off `sub_district` in
  `jurisdictions`; the ten Barshi circles are there but nothing connects a
  village to one. So the taluka figure is what an assessment can use — which is
  also the unit drought is declared at, so it is the right figure to cite, not a
  fallback. Circle rows load behind `--circles` and wait for a mapping.

**Four of eleven Solapur talukas did not match by name** — the portal writes
`N.solapur`, `S.solapur`, `Sangola`, `Mangalwedha` where this platform has
`Solapur North`, `Solapur South`, `Sangole`, `Mangalvedhe`. Their rainfall is
stored with a null jurisdiction and printed at the end of every run. They are
deliberately **not** matched by a transliteration rule: the rule that joins
`Sangola` to `Sangole` would join genuinely different places elsewhere in the
state, and attaching one taluka's rainfall deficit to another's farmers is a
wrong figure that looks right. An explicit alias table, authored by somebody
with local knowledge, is the fix.

### 22. The gauge figure now reaches the panel and the report

Wired through on 29 September. `evidence.Assembler` gathers it as its own source
beside the reanalysis — neither can fail the other — and it reaches the panel in
the evidence packet and §5 of the report.

The weather specialist is now told which source answers which question: **the
state's gauges for how much rain fell and how far below normal, the reanalysis
for which days it fell on**, with an instruction not to average them and not to
present a reanalysis total as a measurement. §5 prints the gauge figure first
with its normal and its monthly rows, and relabels the reanalysis total as
*"Total rainfall (reanalysis model — for comparison)"*.

What changed in the assessment, on the same stored evidence:

> before — all four specialists contradicting the drought claim
> after — *"The weather specialist confirms severe rainfall deficits in the
> area, but both the satellite and vision specialists report no damage."*

The contradiction is now in the right place. The rain failed; this particular
field did not. Damage stays at 0% because NDVI held through September and the
farmer's own footage shows a dense green canopy — most likely irrigated. That is
a finding an officer can act on, where "four specialists contradict the farmer"
was an accusation.

**The mistake worth recording.** I first marked the gauge row `Determines`,
reasoning that it settles whether the rain failed. An existing guard test refused
it — `Determines` in this document means *determines the damage figure*, and the
agreed set is what is read off the photographs plus the issued threshold yield.

The test was right. Barshi is the exact counter-example: the season came to 55%
of normal, August to 16%, and the crop is green. A rainfall deficit that
determined a damage figure would have paid out on that. The row is
`Corroborates` — the strongest corroboration in the section, and still not a
measurement of loss.

**The four taluka aliases** are seeded in migration `0053`, and Solapur now
matches **11 of 11**. Circle names remain unmapped and unmatched by design.

**On being a guest on somebody else's server.** The first version of the loader
would have asked for 70 district-wide reports back to back — every district,
both levels — on first run. These are generated on demand by a public service,
and it is now the only source for a figure farmers' claims depend on, so taking
it down would be both rude and self-defeating. It now requires a named district,
fetches the taluka report only unless asked, refuses to fetch the same district
twice in a day, spaces requests six seconds apart with no concurrency, and
refuses outright past forty reports in one run. The normal daily cost is **one
request per district**.
