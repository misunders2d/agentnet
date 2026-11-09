plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}
val fixtureRun = providers.gradleProperty("agentnetFixtureRun").orNull?.also {
    require(it.matches(Regex("[a-z][a-z0-9]{0,31}"))) { "agentnetFixtureRun must be a lowercase package-safe run name." }
}
// Published preview updates must use their original local key; never commit it.
val previewKeystore = providers.gradleProperty("agentnetPreviewKeystore")
    .orElse(providers.environmentVariable("AGENTNET_PREVIEW_KEYSTORE"))
val releaseVersion = providers.gradleProperty("agentnetReleaseVersion")
    .orElse(providers.environmentVariable("AGENTNET_RELEASE_VERSION"))
val updateVersion = releaseVersion.orElse("v0.8.18").get()
val versionMatch = Regex("^v(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})$").matchEntire(updateVersion)
    ?: error("agentnetReleaseVersion must be vMAJOR.MINOR.PATCH with components below 1000.")
val releaseCode = versionMatch.groupValues.drop(1).map(String::toInt).let { it[0]*1_000_000 + it[1]*1_000 + it[2] }
android {
    namespace = "io.github.misunders2d.agentnet"
    compileSdk = 35
    buildToolsVersion = "35.0.0"
    ndkVersion = "28.2.13676358"
    defaultConfig {
        applicationId = "io.github.misunders2d.agentnet"
        minSdk = 26
        targetSdk = 35
        testInstrumentationRunner = "io.github.misunders2d.agentnet.UpdaterInstrumentation"
        versionCode = 3
        versionName = "0.3.0-comic-preview"
        resValue("string", "app_name", "AgentNet")
        buildConfigField("String", "UPDATE_VERSION", "\"$updateVersion\"")
        buildConfigField("boolean", "SELF_UPDATE", "false")
    }
    buildFeatures { buildConfig = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    signingConfigs.getByName("debug") {
        if (fixtureRun == null && previewKeystore.isPresent) {
            storeFile = file(previewKeystore.get()).also {
                check(it.isFile) { "The existing preview signing key is missing." }
            }
            storePassword = "android"
            keyAlias = "androiddebugkey"
            keyPassword = "android"
        }
    }
    buildTypes {
        debug {
            applicationIdSuffix = fixtureRun?.let { ".fixture.$it" } ?: ".preview"
            buildConfigField("boolean", "SELF_UPDATE", if (fixtureRun == null) "true" else "false")
            resValue("string", "app_name", if (fixtureRun == null) "AgentNet Native Preview" else "AgentNet Release Fixture")
        }
        release { isMinifyEnabled = false }
        create("previewRelease") {
            initWith(getByName("release"))
            applicationIdSuffix = ".preview"
            isDebuggable = false
            signingConfig = signingConfigs.getByName("debug")
            buildConfigField("boolean", "SELF_UPDATE", "true")
            resValue("string", "app_name", "AgentNet Native Preview")
            matchingFallbacks += listOf("release")
        }
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
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517")
}

val verifyPreviewRelease by tasks.registering {
    doLast {
        check(releaseVersion.isPresent && releaseCode > 3) { "Preview releases require an explicit newer agentnetReleaseVersion." }
        check(previewKeystore.isPresent && file(previewKeystore.get()).isFile) { "Preview releases require the original signing keystore." }
    }
}
tasks.configureEach {
    if (name == "prePreviewReleaseBuild") dependsOn(verifyPreviewRelease)
}

androidComponents {
    onVariants(selector().withBuildType("previewRelease")) { variant ->
        variant.outputs.forEach { output ->
            output.versionCode.set(releaseCode)
            output.versionName.set(updateVersion)
        }
    }
}
