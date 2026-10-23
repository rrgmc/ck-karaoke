plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.rrgmc.ckkaraoke"
    compileSdk = 36

    defaultConfig {
        applicationId = "com.rrgmc.ckkaraoke"
        minSdk = 26
        targetSdk = 36
        // versionName is what a person reads; versionCode is what Android
        // compares, and it has to keep going up or an upgrade is refused as a
        // downgrade. They are not the same number and are not meant to be.
        versionCode = 1
        versionName = "1.0"

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

    buildTypes {
        release {
            isMinifyEnabled = false

            // Signed with the debug key, so a release build installs and
            // upgrades like a debug one when sideloaded. Android refuses to
            // upgrade across a change of signing key, and the only way through
            // is an uninstall, which deletes the favorites database. Configure
            // a release key before publishing to a store, and keep it.
            signingConfig = signingConfigs.getByName("debug")
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
