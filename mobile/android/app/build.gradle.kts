plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}
android {
    namespace = "io.github.misunders2d.agentnet"
    compileSdk = 35
    buildToolsVersion = "35.0.0"
    ndkVersion = "28.2.13676358"
    defaultConfig {
        applicationId = "io.github.misunders2d.agentnet"
        minSdk = 26
        targetSdk = 35
        versionCode = 2
        versionName = "0.2.0-comic-preview"
        resValue("string", "app_name", "AgentNet")
    }
    buildFeatures { buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildTypes {
        debug {
            applicationIdSuffix = ".preview"
            resValue("string", "app_name", "AgentNet Native Preview")
        }
        release { isMinifyEnabled = false }
    }
}
val coreAar = layout.projectDirectory.file("libs/agentnet-core.aar")
val verifyCoreAar by tasks.registering {
    doLast {
        check(coreAar.asFile.isFile) {
            "Missing native core AAR. Run mobile/android/scripts/build-core.sh before Gradle."
        }
    }
}
tasks.named("preBuild") { dependsOn(verifyCoreAar) }
dependencies {
    implementation(files(coreAar))
    implementation("androidx.webkit:webkit:1.12.1")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517")
}
