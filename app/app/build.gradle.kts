import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
    id("org.jetbrains.kotlin.plugin.serialization")
}

android {
    namespace = "dev.muxalot"
    compileSdk = 35

    defaultConfig {
        applicationId = "dev.muxalot"
        minSdk = 26
        targetSdk = 35
        // set by the Makefile from the latest git tag (-PversionName / -PversionCode)
        versionCode = providers.gradleProperty("versionCode").map { it.toInt() }.getOrElse(1)
        versionName = providers.gradleProperty("versionName").getOrElse("0.0.0-dev")
    }

    // keys live in app/keystore.properties (gitignored): apk.* signs the GitHub APK, play.* is the Play upload key
    val keys = Properties().apply {
        rootProject.file("keystore.properties").takeIf { it.exists() }?.inputStream()?.use { load(it) }
    }
    signingConfigs {
        for (n in listOf("apk", "play")) {
            if (keys.getProperty("$n.storeFile") != null) create(n) {
                storeFile = rootProject.file(keys.getProperty("$n.storeFile"))
                storePassword = keys.getProperty("$n.storePassword")
                keyAlias = keys.getProperty("$n.keyAlias")
                keyPassword = keys.getProperty("$n.keyPassword")
            }
        }
    }

    // free: dev.muxalot (GitHub). pro: dev.muxalot.pro, the supporter build for Google Play; same source, gated by Edition.isPro
    flavorDimensions += "edition"
    productFlavors {
        create("free") { dimension = "edition" }
        create("pro") {
            dimension = "edition"
            applicationIdSuffix = ".pro"
            versionNameSuffix = "-pro"
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            // -Psigning=apk|play picks the key; unsigned if keystore.properties is absent
            signingConfig = signingConfigs.findByName(providers.gradleProperty("signing").getOrElse("apk"))
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures {
        compose = true
        buildConfig = true
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2024.10.01")
    implementation(composeBom)
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    // QR scanning for pairing (Apache-2.0)
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
}
