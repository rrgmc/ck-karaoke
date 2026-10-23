plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// The version comes from version/VERSION, the same file the Go code embeds.
// The version code is MAJOR*10000 + MINOR*100 + PATCH, so it rises with every
// release; a suffix such as -rc1 does not change it.
val appVersion = rootDir.resolve("../../version/VERSION").readText().trim()
val appVersionCode = Regex("""^(\d+)\.(\d+)\.(\d+)""").find(appVersion)
    ?.destructured
    ?.let { (major, minor, patch) -> major.toInt() * 10000 + minor.toInt() * 100 + patch.toInt() }
    ?: error("version/VERSION holds \"$appVersion\", not MAJOR.MINOR.PATCH")

android {
    namespace = "com.rrgmc.ckkaraoke"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.rrgmc.ckkaraoke"
        minSdk = 26
        targetSdk = 36
        // versionName is what a person reads; versionCode is what Android
        // compares, and it has to keep going up or an upgrade is refused as a
        // downgrade. Both come from version/VERSION; -PversionName and
        // -PversionCode override them.
        versionCode = (findProperty("versionCode") as String?)?.toInt() ?: appVersionCode
        versionName = (findProperty("versionName") as String?) ?: appVersion

        // The server binary is built only for arm64: of the Android targets,
        // that is the one Go links without cgo, and it covers current phones.
        ndk { abiFilters += "arm64-v8a" }
    }

    packaging {
        jniLibs {
            // The binary has to be a real file on disk for the app to execute
            // it. Left compressed inside the APK, there is nothing to exec.
            useLegacyPackaging = true
        }
    }

    // The release key, when the environment provides one. CI decodes it from
    // repository secrets; see .github/workflows/release.yml.
    val releaseKeystore = System.getenv("CK_KEYSTORE_FILE")
    if (releaseKeystore != null) {
        signingConfigs {
            create("release") {
                storeFile = file(releaseKeystore)
                storePassword = System.getenv("CK_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("CK_KEY_ALIAS")
                keyPassword = System.getenv("CK_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false

            // Signed with the release key when one is configured, otherwise
            // with the debug key. Android refuses to upgrade across a change of
            // signing key, and the only way through is an uninstall, which
            // deletes the favorites database, so published builds must always
            // use the same release key.
            signingConfig = if (releaseKeystore != null) {
                signingConfigs.getByName("release")
            } else {
                signingConfigs.getByName("debug")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    sourceSets["main"].java.srcDir("src/main/kotlin")
}

kotlin {
    // kotlinOptions.jvmTarget was removed in Kotlin 2.4; this is its
    // replacement, and it has to agree with compileOptions above.
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

// No dependencies at all: the app is an Activity, a Service and a WebView, all
// from the framework. That keeps the build offline and the APK small next to
// the 14MB server it carries.
dependencies {}
