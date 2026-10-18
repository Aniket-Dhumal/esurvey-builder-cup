# What has to be checked on a handset

Everything in this file is behaviour that no test on a laptop can settle: it
depends on Android stopping a process, a real radio losing signal, a camera
holding memory, or a keystore shared by two processes. Each check says how to
force the condition, what should happen, and how to see whether it did.

Written 2026-09-16, after the sync work (FARMER_JOURNEY_PLAN.md phase 1) and
the scale fixes (SYNC_SCALE_REVIEW.md, PLATFORM_SCALE_REVIEW.md). Record the
result of each run in the table at the end -- "verified" means somebody ran it
and wrote down what they saw, which is the word this project now reserves for
that (see FLOW_REBUILD.md for why).

## What is already proven without a handset, and what is not

Most of the *logic* under these checks has automated tests; what those tests
cannot exercise is the platform underneath -- Android stopping a process, a
real radio losing signal, a camera holding memory, two isolates sharing a
keystore. Each check below says which part the handset is actually for, so
nobody spends a morning re-proving what a test already covers.

| Check | Already automated | What the handset is for |
|---|---|---|
| 1 Offline claim | queue, ordering, retry (`test/sync_test.dart`) | the camera, GPS and the walk with no signal |
| 2 Killed mid-form / mid-walk | journey resume (`test/journey_test.dart`) | Android killing the process for real |
| 3 Flat battery | as above | power loss mid-write |
| 4 Expired login | lapse keeps the queue (`test/owners_test.dart`, `internal/auth`) | the real 30-day token, on the real keystore |
| 5 Two processes refreshing | grace window (`internal/auth/session_test.go`), client (`test/scale_gaps_test.dart`) | two real isolates, one keystore |
| 6 Slow-link resume | chunk resume (`test/uploader_test.dart`, `internal/api/upload_e2e_test.go`) | a real radio dropping mid-chunk |
| 7 Worker stopped mid-upload | the 8-minute budget (`test/sync_test.dart`) | WorkManager actually stopping it |
| 8 Forgotten upload | sweep then restart, end to end (`upload_e2e_test.go`) | the same over a real connection |
| 9 Shared handset | hidden queues, purge after send (`test/owners_test.dart`) | two farmers, one phone, real sign-in |
| 10 Memory while recording | -- | the whole point: a 2 GB handset |

Set the environment up with `scripts/handset-check.sh` (it prints the adb and
psql commands each check needs, and refuses to guess the ones that touch a
farmer's data).

## Before starting

    cd mobile-app && flutter run --release        # a release build, not debug
    adb logcat -s flutter                          # the app's own lines

Point the build at a server you can restart and read logs from:
`--dart-define=ESURVEY_API_BASE=https://<host>`.

Useful while testing:

    adb shell am force-stop ai.agribid.esurvey        # kill the app outright
    adb shell cmd appops set ai.agribid.esurvey RUN_ANY_IN_BACKGROUND deny
    adb shell dumpsys deferredjobs | grep esurvey    # is work scheduled
    adb shell settings put global airplane_mode_on 1 # and 0 to restore
    adb shell am broadcast -a android.intent.action.AIRPLANE_MODE

---

## 1. A claim filed with no signal at all

1. Airplane mode on, before opening the app.
2. File a claim: pick the parcel, fill the form, walk the four stations.
3. Home should show the claim waiting, with "safe on this phone".
4. Airplane mode off. Within a minute the queue should drain: report first,
   then photographs, then clips.

**Watch for:** any screen that blocks on the network; a station walk that
falls back to the photograph sequence (that means the parcel had no cached
stations -- check the parcel was prepared while online).

## 2. The app is killed mid-form and mid-walk

1. Start a claim, fill in half the form, `force-stop`.
2. Reopen. Home should offer "Continue", and the form should come back with
   what was typed.
3. Reach station 3 of the walk, `force-stop` during the clip.
4. Reopen, continue. It must resume at the point without its clip -- never at
   station 1, never in the photograph sequence.

## 3. A flat battery during the walk

Let the phone die (or `adb shell svc power stayon false` and drain it) between
two stations. On charge and reopen: same as check 2 -- the walk resumes at the
station that has no clip.

## 4. An expired or refused login

On the server, revoke the phone's session (or set
`ESURVEY_FARMER_REFRESH_TTL=1m`, restart, wait).

1. The app should show sign-in saying the claim is waiting and nothing is lost.
2. The queue must still be on the phone: sign in as the same farmer, and the
   claim should send without being re-entered.

**Must not happen:** the queue emptying, or the claim disappearing from Home.

## 5. Two processes refreshing at once

This is the one that used to sign farmers out (SYNC_SCALE_REVIEW.md, C4).

1. Leave a claim queued and the app open.
2. Force the background worker to run while the app is in the foreground:
   `adb shell cmd jobscheduler run -f ai.agribid.esurvey <job id>` (the id is in
   `dumpsys deferredjobs`).
3. Both processes should keep working; the farmer stays signed in.

**How to see it went right:** the server's audit log has `auth.refresh`
entries and no `auth.refresh.reuse`; the app does not show the sign-in screen.

## 6. A clip uploading over a real slow link

1. Queue a station clip (about 12 MB).
2. Throttle the connection (`adb shell cmd netpolicy set restrict-background
   true`, or a 2G-only APN, or walk out of coverage).
3. Send, then interrupt: airplane mode on mid-upload.
4. Back online, the upload must resume from the chunk the server has -- not
   from zero. Watch the bytes in the app's queue card, and
   `capture_uploads.received_bytes` on the server.

## 7. The background worker is stopped by Android mid-upload

1. Start a large clip uploading with the app in the background.
2. Let Android stop the worker (it does at about ten minutes), or
   `adb shell cmd jobscheduler timeout ai.agribid.esurvey <job id>`.
3. The next pass -- foreground or background -- must carry on from where it
   stopped, and the app must not be blocked waiting for a lease held by the
   killed worker for more than a few minutes.

**What was fixed here:** a pass now stops itself after 8 minutes and renews the
lease after every chunk (H5).

## 8. An upload the server has forgotten

1. Start a clip; let two chunks land.
2. On the server: `UPDATE capture_uploads SET expires_at = now() - interval
   '1 day' WHERE id = '<id>';` then wait for the hourly sweep (or restart the
   API, which sweeps at startup).
3. The next chunk gets a 404. The app must begin the upload again and finish
   it -- not mark the clip failed.

## 9. A shared handset

1. Farmer A files a claim with no signal, then signs out. The warning should
   say the work will keep sending and be removed once it arrives.
2. Farmer B signs in. B must see nothing of A's: no claims, no queue card, no
   land records.
3. Restore the network. A's claim should reach the office and then disappear
   from the phone (check `crop_loss_reports` for A's claim and the app's
   storage for A's files).
4. Sign B out, sign A back in: A should see their claim as sent.

## 10. Memory while recording

Ten stations' worth of recording on the cheapest handset in the pilot (2 GB
RAM). The app must not be killed while saving a clip.

    adb shell dumpsys meminfo ai.agribid.esurvey | head -20

**What was fixed here:** clips are hashed as a stream rather than read whole
(H4). A kill at the moment of saving is this regressing.

---

## Results

| # | Check | Date | Handset | Result | Notes |
|---|---|---|---|---|---|
| 1 | Offline claim | | | | |
| 2 | Killed mid-form / mid-walk | | | | |
| 3 | Flat battery | | | | |
| 4 | Expired login | | | | |
| 5 | Two processes refreshing | | | | |
| 6 | Slow-link resume | | | | |
| 7 | Worker stopped mid-upload | | | | |
| 8 | Forgotten upload | | | | |
| 9 | Shared handset | | | | |
| 10 | Memory while recording | | | | |
