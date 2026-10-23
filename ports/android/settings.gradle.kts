pluginManagement {
    repositories {
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

// Lets Gradle download a real JDK for compilation. Necessary because Android
// Studio's bundled runtime reports a malformed version —
// "25.0.2+-15348964-b329.117", with no build number before the dash — which
// IntelliJ's parser, and therefore the Kotlin compiler, refuses outright. The
// Gradle daemon can keep running on it; the compiler cannot.
plugins {
    id("org.gradle.toolchains.foojay-resolver-convention") version "0.8.0"
}

dependencyResolutionManagement {
    repositories {
        google()
        mavenCentral()
    }
}

rootProject.name = "ckkaraoke"
include(":app")
