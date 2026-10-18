#!/usr/bin/env bash
# Set up one of the handset checks in docs/HANDSET_CHECKS.md.
#
# It does the fiddly, repeatable half -- finding the app's background job,
# throttling the radio, putting an upload past its expiry -- and stops short of
# anything that decides something. Nothing here signs anybody out, deletes a
# claim, or writes to a farmer's record: those are the acts the check is
# testing, and they belong to the person running it.
#
#   scripts/handset-check.sh list
#   scripts/handset-check.sh 2            # prepare check 2, print its steps
#   scripts/handset-check.sh watch        # follow the app's own log lines
#
# Requires adb on the PATH and one device attached. Checks that touch the
# server also want DATABASE_URL set to the deployment under test.

set -euo pipefail

APP=ai.agribid.esurvey

die() { printf '%s\n' "$*" >&2; exit 1; }

need_device() {
  command -v adb >/dev/null || die "adb is not on the PATH"
  local n
  n=$(adb devices | grep -c "device$" || true)
  [ "$n" = "1" ] || die "attach exactly one device (adb devices shows $n)"
}

jobs_for_app() {
  adb shell dumpsys deferredjobs 2>/dev/null | grep -i "$APP" || true
}

case "${1:-list}" in
list)
  cat <<'TXT'
The checks, from docs/HANDSET_CHECKS.md:

  1  a claim filed with no signal at all
  2  the app killed mid-form and mid-walk
  3  a flat battery during the walk
  4  an expired or refused login
  5  two processes refreshing at once
  6  a clip uploading over a real slow link
  7  the background worker stopped mid-upload
  8  an upload the server has forgotten
  9  a shared handset
  10 memory while recording

Run `scripts/handset-check.sh <n>` to prepare one.
TXT
  ;;

1)
  need_device
  echo "Putting the phone into airplane mode. Turn it off yourself when the"
  echo "claim is filed -- the point of the check is what happens when it returns."
  adb shell settings put global airplane_mode_on 1
  adb shell am broadcast -a android.intent.action.AIRPLANE_MODE >/dev/null
  echo
  echo "Then: file a claim, walk the four stations, and watch the queue card."
  echo "To restore:  adb shell settings put global airplane_mode_on 0"
  ;;

2|3)
  need_device
  echo "Fill in half the form (or reach station 3), then:"
  echo "  adb shell am force-stop $APP"
  echo "Reopen the app. Home should offer Continue, and it must resume at the"
  echo "step you left -- never at station 1, never in the photograph sequence."
  ;;

4)
  cat <<'TXT'
On the server, end this phone's session. Either:

  * revoke it from the console (Accounts -> the farmer -> sign out), or
  * set ESURVEY_FARMER_REFRESH_TTL=1m, restart the API, and wait a minute.

Then open the app. It must show sign-in, say the claim is waiting, and keep
the queue. Signing in as the same farmer must send it without re-entering
anything.

Deliberately not automated here: revoking somebody's session is a decision.
TXT
  ;;

5)
  need_device
  echo "The app's background job, so it can be run while the app is open:"
  jobs_for_app
  echo
  echo "Force it:   adb shell cmd jobscheduler run -f $APP <job id>"
  echo "Then watch the server's audit log for auth.refresh entries, and make"
  echo "sure no auth.refresh.reuse appears. The farmer must stay signed in."
  ;;

6)
  need_device
  echo "Throttling background data. Start a clip uploading, then turn the"
  echo "radio off mid-upload with check 1's airplane command."
  adb shell cmd netpolicy set restrict-background true || true
  echo
  echo "The upload must resume from the chunk the server has. On the server:"
  echo "  SELECT received_bytes, total_bytes, next_chunk FROM capture_uploads"
  echo "   WHERE capture_id IS NULL ORDER BY updated_at DESC LIMIT 5;"
  echo "To restore:  adb shell cmd netpolicy set restrict-background false"
  ;;

7)
  need_device
  echo "Start a large clip uploading with the app in the background, then:"
  jobs_for_app
  echo "  adb shell cmd jobscheduler timeout $APP <job id>"
  echo
  echo "The next pass must carry on from where it stopped, and the app must not"
  echo "be blocked for more than a few minutes by the killed worker's lease."
  ;;

8)
  cat <<'TXT'
Let two chunks land, then put the upload past its expiry on the server:

  UPDATE capture_uploads SET expires_at = now() - interval '1 day'
   WHERE id = '<upload id>';

Wait for the hourly sweep, or restart the API (it sweeps at startup). The next
chunk answers 404 UPLOAD_NOT_FOUND, and the app must begin the upload again
and finish it -- not mark the clip failed.

The same path is covered end to end by TestAClipSurvivesEverythingBetweenThe
HandsetAndTheStore; what this adds is a real connection.
TXT
  ;;

9)
  cat <<'TXT'
Two farmers, one phone:

  1. Farmer A files a claim with no signal (check 1), then signs out. The
     warning must say the work keeps sending and is removed once it arrives.
  2. Farmer B signs in. B must see none of A's claims, queue or land.
  3. Restore the network. A's claim should reach the office and then disappear
     from the phone.
  4. Sign B out, sign A back in: A sees the claim as sent.

Not automated here: it is two real sign-ins, and the codes go to two phones.
TXT
  ;;

10)
  need_device
  echo "Watching memory while you record. Record ten stations' worth on the"
  echo "cheapest handset in the pilot; the app must not be killed while saving."
  echo
  while true; do
    adb shell dumpsys meminfo "$APP" 2>/dev/null | awk '/TOTAL PSS|TOTAL RSS/ {print strftime("%H:%M:%S"), $0}'
    sleep 5
  done
  ;;

watch)
  need_device
  adb logcat -s flutter
  ;;

*)
  die "unknown check: $1 (try: scripts/handset-check.sh list)"
  ;;
esac
