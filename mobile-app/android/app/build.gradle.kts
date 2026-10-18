import java.util.Properties

plugins {
    id("com.android.application")
    id("kotlin-android")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

// The signing credentials, or null when this machine does not have them.
val keystoreProperties: Properties? =
    rootProject.file("key.properties").takeIf { it.exists() }?.let { file ->
        Properties().apply { file.inputStream().use { load(it) } }
    }

android {
    namespace = "ai.agribid.esurvey"
    // 37, because flutter_secure_storage compiles against it and that is the
    // plugin holding this app's refresh tokens -- compiling its host against
    // an older SDK is the kind of mismatch that surfaces as a keystore call
    // behaving differently on one Android version.
    //
    // This needs a platform directory named "android-37". The SDK manager
    // installs it as "android-37.0" and Gradle matches the name literally, so
    // a junction bridges the two:
    //
    //     mklink /J "%LOCALAPPDATA%\Android\Sdk\platformsndroid-37" ^
    //               "%LOCALAPPDATA%\Android\Sdk\platformsndroid-37.0"
    //
    // Anyone building without that junction gets a clear "failed to find
    // Platform SDK with path: platforms;android-37", which is a better failure
    // than a silent version skew inside the credential store.
    //
    // Compiling against 37 changes nothing about which handsets this runs on:
    // that is minSdk's job, and minSdk is 23.
    compileSdk = 37
    ndkVersion = flutter.ndkVersion

    compileOptions {
        // flutter_local_notifications compiles against java.time, which does
        // not exist on the Android 7 and 8 handsets this app supports
        // (minSdk 24). Desugaring backports it into the APK; without this the
        // build is refused outright by the AAR metadata check.
        isCoreLibraryDesugaringEnabled = true
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = JavaVersion.VERSION_17.toString()
    }

    defaultConfig {
        applicationId = "ai.agribid.esurvey"
        // 24, which is Android 7.0, and it is a floor set by dependencies
        // rather than a preference.
        //
        // This block used to claim 23, so that Android 6 handsets "still in
        // use in rural Maharashtra" would be supported. That was not true and
        // could not be made true: flutter_tts declares minSdkVersion 24, and
        // the manifest merger refuses the build outright at anything lower.
        // The voice guidance is not optional on this app -- it is how a farmer
        // who cannot read the screen is walked across their own field -- so 24
        // is what the app costs.
        //
        // The line read `flutter.minSdkVersion` for a while, which resolved to
        // 24 and worked, beneath a comment insisting on 23. Stated as the
        // literal now so the number is visible and cannot drift, and so the
        // next person to read it is told the real constraint.
        //
        // flutter_secure_storage v11 needs 23 for keystore-backed storage; 24
        // clears that comfortably, so refresh tokens still have somewhere safe
        // to live.
        minSdk = 24
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
    }

    signingConfigs {
        // The upload key lives in android/key.properties, which is gitignored
        // and is the one file in this repository that must never be committed.
        // Absent -- on a fresh clone, on CI, on any machine but the signer's --
        // the block below simply does not run and release falls back to debug
        // keys, so `flutter run --release` still works for anybody testing.
        if (keystoreProperties != null) {
            create("release") {
                keyAlias = keystoreProperties.getProperty("keyAlias")
                keyPassword = keystoreProperties.getProperty("keyPassword")
                storeFile = keystoreProperties.getProperty("storeFile")
                    ?.let { rootProject.file(it) }
                storePassword = keystoreProperties.getProperty("storePassword")
            }
        }
    }

    buildTypes {
        release {
            // A debug-signed APK cannot be updated by a Play-signed one and
            // vice versa: the signature is the identity Android upgrades
            // against. So this falls back rather than failing -- but it warns,
            // because a debug-signed build handed to a farmer is a build that
            // can never be upgraded in place.
            signingConfig = if (keystoreProperties != null) {
                signingConfigs.getByName("release")
            } else {
                logger.warn(
                    "esurvey: no android/key.properties -- signing the release " +
                    "build with DEBUG keys. Fine for testing, not distributable."
                )
                signingConfigs.getByName("debug")
            }

            // R8 on, because the app ships to handsets where storage is the
            // scarce resource and a smaller APK is one a farmer can actually
            // download over a patchy connection.
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }
}

dependencies {
    // The backported java.time and friends, for the compileOptions above.
    coreLibraryDesugaring("com.android.tools:desugar_jdk_libs:2.1.5")
}

flutter {
    source = "../.."
}
