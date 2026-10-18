import 'package:flutter/foundation.dart';

/// What a farmer is told when something fails.
///
/// # Why the server's words are not used
///
/// `Result.message` used to return the API's own message, on the reasoning
/// that it was "written to be read by the person who caused the problem". It
/// is not. The API is written for programs, so it says `farmer_id is required`
/// and `the access token has expired; refresh it`, and until the guard was
/// added on the server it would also hand over `json: cannot unmarshal number
/// into Go struct field officerLoginRequest.username of type string`. None of
/// that means anything to somebody standing in a field, and none of it is in
/// their language.
///
/// # Why the same thing was happening locally
///
/// Eleven screens appended a caught Dart exception to their own message —
/// `'Could not save. $e'` — which put `PlatformException(camera_error, ...)`
/// and `SocketException: OS Error: Connection refused, errno = 111` in front
/// of a farmer. The raw error belongs in the log, where a developer can read
/// it, and nowhere else.
///
//// # Stored in both languages, shown in one
///
/// Every string here is Marathi, a middle dot, then English, and each half is
/// a whole sentence. The pair is what the queue stores, so a message saved
/// today still reads correctly if the region's language changes; a screen
/// shows the region's half through Language.of.

const _byCode = <String, String>{
  // Signing in.
  'INVALID_CREDENTIALS': 'दिलेली माहिती बरोबर नाही. · That is not correct.',
  'OTP_INVALID':
      'कोड बरोबर नाही. नवीन कोड मागवा. · That code is not correct. Ask for a new one.',
  'OTP_COOLDOWN':
      'कोड आत्ताच पाठवला आहे. थोडे थांबून पुन्हा मागवा. · A code was just sent. Wait before asking again.',
  'OTP_ATTEMPTS':
      'कोड खूप वेळा चुकला. नवीन कोड मागवा. · Too many wrong codes. Ask for a new one.',
  'OTP_DAILY_CAP':
      'आजच्या कोडची मर्यादा संपली. उद्या प्रयत्न करा. · Too many codes today. Try tomorrow.',
  'LOCKED_OUT':
      'खूप प्रयत्न झाले. थोड्या वेळाने प्रयत्न करा. · Too many attempts. Try again later.',
  'ACCOUNT_INACTIVE':
      'हे खाते आता सुरू नाही. · This account is no longer active.',
  'NO_USABLE_MOBILE':
      'कोणताही मोबाईल नंबर नोंदलेला नाही. · No mobile number is on record.',

  // Session.
  'AUTH_REQUIRED':
      'पुढे जाण्यासाठी पुन्हा लॉगिन करा. · Sign in again to continue.',
  'TOKEN_EXPIRED':
      'तुमचे सत्र संपले. पुन्हा लॉगिन करा. · Your session has ended. Sign in again.',
  'TOKEN_INVALID':
      'तुमचे सत्र संपले. पुन्हा लॉगिन करा. · Your session has ended. Sign in again.',
  'SESSION_REVOKED':
      'हे सत्र बंद केले आहे. पुन्हा लॉगिन करा. · This session was ended. Sign in again.',

  // Permission and ownership.
  'FORBIDDEN': 'हे करण्याची परवानगी नाही. · You cannot do that.',
  'FARMER_ONLY': 'हे फक्त शेतकरीच करू शकतात. · Only the farmer can do that.',
  'NOT_YOUR_PARCEL':
      'हे शेत दुसऱ्या शेतकऱ्याच्या नावावर आहे. · This plot is on another farmer’s record.',
  'PARCEL_NOT_OWNED':
      'हे शेत दुसऱ्या शेतकऱ्याच्या नावावर आहे. · This plot is on another farmer’s record.',
  'NOT_YOUR_REPORT':
      'ही नोंद दुसऱ्या शेतकऱ्याची आहे. · This report is another farmer’s.',

  // Not there.
  'FARMER_NOT_FOUND':
      'या क्रमांकाचा शेतकरी सापडला नाही. · No farmer with that ID.',
  'PARCEL_NOT_FOUND':
      'हे शेत तुमच्या नोंदीत नाही. · That plot is not on your record.',
  'NO_PARCEL':
      'तुमच्या नावावर कोणतेही शेत नोंदलेले नाही. · No plot is on your record.',
  'REPORT_NOT_FOUND':
      'ही नोंद आता उपलब्ध नाही. · That report no longer exists.',
  'EVENT_NOT_FOUND':
      'या आपत्तीसाठी नोंदणी बंद झाली आहे. · That survey is no longer open.',
  'CAPTURE_NOT_FOUND':
      'हा फोटो नोंदीत नाही. · That photo is not on the report.',
  'UPLOAD_NOT_FOUND':
      'पाठवणे अपूर्ण राहिले. पुन्हा पाठवले जाईल. · That upload expired. Send it again.',

  // Where you are standing.
  'OUTSIDE_PARCEL':
      'तुम्ही या शेताच्या बाहेर आहात. · You are standing outside this plot.',
  'NOT_IN_EVENT_AREA':
      'हे शेत आपत्ती जाहीर झालेल्या भागाबाहेर आहे. · This plot is outside the survey area.',
  'MOCKED_POSITION': 'हे ठिकाण खरे नाही. · This location is not genuine.',
  'POSITION_TOO_COARSE':
      'ठिकाण पुरेसे अचूक नाही. थोडे थांबा. · The location is not accurate enough. Wait a moment.',
  'MISSING_POSITION':
      'फोनकडून ठिकाण मिळाले नाही. · The phone did not give a location.',

  // What was captured.
  'MISSING_IMAGE': 'आधी फोटो जोडा. · Attach a photo first.',
  'UNUSABLE_MEDIA': 'ही फाइल वाचता आली नाही. · That file could not be read.',
  'BAD_MEDIA': 'ही फाइल वाचता आली नाही. · That file could not be read.',
  'NO_TRACED_BOUNDARY':
      'आधी शेताची हद्द नोंदवा. · Walk the edge of the plot first.',
  'NO_BOUNDARY': 'आधी शेताची हद्द नोंदवा. · Walk the edge of the plot first.',
  'NOTHING_MEASURED':
      'काहीच मोजलेले नाही. मोजणी किंवा नमुना नोंदवा. · Record a count or a sample.',

  // State.
  'ALREADY_RECORDED': 'हे आधीच नोंदलेले आहे. · That is already recorded.',
  'INVALID_STATE': 'हे आता बदलता येणार नाही. · This cannot be changed now.',
  'GRIEVANCE_ALREADY_OPEN':
      'तक्रार आधीच दाखल आहे. · A complaint is already open.',
  'NOT_APPEALABLE':
      'या निर्णयावर आक्षेप घेता येणार नाही. · This decision cannot be appealed.',
  // A closed survey window, an ineligible report, an out-of-window claim: the
  // API answers all three with NOT_ELIGIBLE, so the sentence has to cover the
  // shape of the refusal rather than one of its causes.
  'NOT_ELIGIBLE':
      'हे आता स्वीकारता येणार नाही. नोंदणी बंद झाली असू शकते. · This cannot be accepted. The survey may have closed.',
  'INVALID_REPORT':
      'नोंदीतील काही माहिती वापरता येत नाही. · Some details on this report are not usable.',
  'EVENT_CLOSED':
      'या आपत्तीसाठी नोंदणी बंद झाली आहे. · Surveying for this event has closed.',

  // The office refused a clip because it was recorded where another one already
  // was. Not a fault in the recording, and not something retrying can fix: the
  // point has to be walked again somewhere else.
  //
  // It was reaching the farmer as the generic refusal, which told them nothing
  // they could act on -- and the clip was discarded, so a walk of four came to a
  // claim of one.
  'SAME_POSITION':
      'ही नोंद आधीच्या नोंदीच्या जागीच घेतली गेली आहे. शेतात दुसऱ्या '
      'ठिकाणी जाऊन हा मुद्दा पुन्हा नोंदवा. · This was recorded where an '
      'earlier point already was. Walk to another part of the field and record '
      'this point again.',

  // Ground another claim on the same survey number already filmed.
  //
  // A survey number is routinely shared: one farmer's soybean and udad are two
  // claims on it, and a neighbour's holding is a third. Worded as the farmer's
  // own share -- वाटणी -- because that is the remedy, and it is a word they use
  // about exactly this. "Walk further" would be wrong advice here: the ground is
  // somebody else's evidence however far along it they go.
  'POINT_TAKEN_IN_SURVEY':
      'तुमच्या सर्व्हे नंबरमध्ये या ठिकाणी आधीच चित्रीकरण झाले आहे. तुमच्या '
      'वाटणीत जाऊन व्हिडिओ काढा. · This spot has already been recorded under '
      'your survey number. Record on your own share of it.',

  // Uploads and load.
  'UPLOAD_TOO_LARGE': 'फाइल खूप मोठी आहे. · That file is too large.',
  'TOO_LARGE': 'फाइल खूप मोठी आहे. · That file is too large.',
  'UPLOAD_CORRUPT':
      'फाइल पूर्ण पोहोचली नाही. पुन्हा पाठवा. · It did not arrive whole. Send it again.',
  'TOO_MANY_UPLOADS':
      'आधीचे पाठवणे पूर्ण होईपर्यंत थांबा. · Wait for the current uploads to finish.',
  'RATE_LIMITED':
      'खूप विनंत्या झाल्या. थोडे थांबा. · Too many requests. Wait a moment.',
  'ASSEMBLY_BUSY':
      'कार्यालय व्यस्त आहे. आपोआप पाठवले जाईल. · The office is busy. It will send by itself.',
  'UPLOAD_RESTARTED':
      'फाइल पूर्ण पोहोचली नाही. पुन्हा पाठवत आहे. · It did not arrive whole. Sending again.',
  'IDEMPOTENCY_IN_PROGRESS':
      'हे आधीच पाठवले जात आहे. थोडे थांबा. · Already being sent. Wait a moment.',
  'IDEMPOTENCY_UNAVAILABLE':
      'फोनवर जतन आहे. नंतर पाठवले जाईल. · Saved on the phone. It will send later.',
  'UPGRADE_REQUIRED':
      'पाठवण्यासाठी ॲप अपडेट करा. तुमचे काम जतन आहे. · Update the app to send. Your work is saved.',

  // Services.
  'SATELLITE_UNAVAILABLE':
      'सेवा सध्या उपलब्ध नाही. नंतर प्रयत्न करा. · A service is down. Try later.',
  'AI_UNAVAILABLE':
      'सेवा सध्या उपलब्ध नाही. नंतर प्रयत्न करा. · A service is down. Try later.',
  'AI_UNREACHABLE':
      'सेवा सध्या उपलब्ध नाही. नंतर प्रयत्न करा. · A service is down. Try later.',
  'IDENTITY_UNAVAILABLE':
      'सेवा सध्या उपलब्ध नाही. नंतर प्रयत्न करा. · A service is down. Try later.',
};

/// One sentence per HTTP status, for codes not in the table.
///
/// The API has 122 codes and will grow more; a missing entry must degrade to
/// something a person can still act on, never to a number.
String messageForStatus(int status) {
  if (status == 0) {
    return 'नेटवर्क नाही. हे फोनवर जतन आहे. · No network. This is saved on the phone.';
  }
  if (status == 401 || status == 403) {
    return 'पुढे जाण्यासाठी पुन्हा लॉगिन करा. · Sign in again to continue.';
  }
  if (status == 404) return 'हे सापडले नाही. · That could not be found.';
  if (status == 409) {
    return 'हे आधीच बदलले आहे. पुन्हा उघडा. · This changed already. Reload.';
  }
  if (status == 413) return 'फाइल खूप मोठी आहे. · That file is too large.';
  if (status == 429) {
    return 'खूप प्रयत्न झाले. थोडे थांबा. · Too many attempts. Wait a moment.';
  }
  if (status >= 500) {
    return 'काहीतरी चुकले. नंतर प्रयत्न करा. · Something went wrong. Try later.';
  }
  if (status >= 400) {
    return 'माहिती तपासून पुन्हा प्रयत्न करा. · Check the details and try again.';
  }
  return 'हे पोहोचले नाही. पुन्हा प्रयत्न करा. · That did not go through. Try again.';
}

// What to show for an API failure.
String messageForCode(String? code, int status) {
  final m = code == null ? null : _byCode[code];
  return m ?? messageForStatus(status);
}

/// What to show when the phone itself failed — a camera, a file, a sensor.
///
/// The exception goes to the log with [context] so it is still debuggable; the
/// farmer gets a sentence. Returning the exception text "just in case it
/// helps" is how `PlatformException(camera_error, ...)` ends up on screen, and
/// it has never once helped the person reading it.
String deviceError(String marathi, String english, Object e, String context) {
  debugPrint('[$context] $e');
  return '$marathi · $english';
}
