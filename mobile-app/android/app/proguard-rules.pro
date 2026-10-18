# R8 keep rules for the release build.
#
# Flutter's own engine rules and each plugin's consumer rules are applied
# automatically -- an AAR ships the rules its own code needs. What is listed
# here is only what R8 cannot see for itself, and each entry says why, because
# a keep rule with no reason is one nobody can ever safely delete.

# Flutter's deferred-components machinery is referenced from the engine by
# name. It is not used here, and without these R8 warns about the dangling
# references rather than failing -- but a warning nobody can act on is noise
# in every future release build.
-dontwarn io.flutter.embedding.engine.deferredcomponents.**
-dontwarn io.flutter.embedding.android.FlutterPlayStoreSplitApplication

# Keep the line numbers. A stack trace from a farmer's handset is the only
# report of a crash that will ever exist -- nobody is going to reproduce it
# on request -- so an unreadable one costs the whole bug.
-keepattributes SourceFile,LineNumberTable
-renamesourcefileattribute SourceFile
