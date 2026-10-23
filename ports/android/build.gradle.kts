plugins {
    id("com.android.application") version "8.13.2" apply false
    // 2.4.10 rather than the 2.0.21 sitting in the local cache: that version's
    // compiler cannot parse the JDK 25 version string Android Studio ships, and
    // fails with a bare "IllegalArgumentException: 25.0.2". Every JDK on this
    // machine is 25, so the plugin is the side that has to move.
    id("org.jetbrains.kotlin.android") version "2.4.10" apply false
}
