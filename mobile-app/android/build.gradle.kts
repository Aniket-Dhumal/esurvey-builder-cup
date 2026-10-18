allprojects {
    repositories {
        google()
        mavenCentral()
    }
}

val newBuildDir: Directory =
    rootProject.layout.buildDirectory
        .dir("../../build")
        .get()
rootProject.layout.buildDirectory.value(newBuildDir)

subprojects {
    val newSubprojectBuildDir: Directory = newBuildDir.dir(project.name)
    project.layout.buildDirectory.value(newSubprojectBuildDir)
}
// Every plugin asks for flutter.compileSdkVersion on its own, so pinning the
// app alone leaves them looking for android-37 and failing one by one. Forced
// here so the whole build agrees on a platform that is actually installed.
//
// Hooked on the plugin being applied rather than in afterEvaluate: the block
// below evaluates :app eagerly, so by the time an afterEvaluate would run the
// project is already evaluated and Gradle refuses the callback outright.
subprojects {
    plugins.withId("com.android.library") {
        val android = project.extensions.findByName("android")
        if (android is com.android.build.gradle.BaseExtension) {
            android.compileSdkVersion(36)
        }
    }
}

subprojects {
    project.evaluationDependsOn(":app")
}

tasks.register<Delete>("clean") {
    delete(rootProject.layout.buildDirectory)
}
