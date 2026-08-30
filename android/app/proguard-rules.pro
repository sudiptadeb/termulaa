# ── kotlinx-serialization ────────────────────────────────────────────────────
# R8 full mode strips the generated serializer lookup chain unless the
# @Serializable classes' Companion + serializer() members are kept. These are
# the official rules from the kotlinx.serialization README, scoped to this app.

# Keep `Companion` object fields of serializable classes, so that the
# serializer can be looked up via the companion.
-if @kotlinx.serialization.Serializable class **
-keepclassmembers class <1> {
    static <1>$Companion Companion;
}

# Keep `serializer()` on companion objects of serializable classes.
-if @kotlinx.serialization.Serializable class ** {
    static **$Companion Companion;
}
-keepclassmembers class <1>$Companion {
    kotlinx.serialization.KSerializer serializer(...);
}

# Keep `INSTANCE.serializer()` of serializable objects.
-if @kotlinx.serialization.Serializable class ** {
    public static ** INSTANCE;
}
-keepclassmembers class <1> {
    public static <1> INSTANCE;
    kotlinx.serialization.KSerializer serializer(...);
}

# Serializer implementations are looked up reflectively in a few paths.
-keepclassmembers class **$$serializer {
    *** INSTANCE;
}

-keepattributes RuntimeVisibleAnnotations,AnnotationDefault,Signature,InnerClasses,EnclosingMethod

# Our wire models: keep names+members outright. The set is tiny (7 classes),
# so the APK cost is negligible and it removes a whole class of minification
# surprises for the frozen server contract.
-keep,includedescriptorclasses class com.debkosh.termulaa.data.** { *; }

# ── OkHttp / Okio ────────────────────────────────────────────────────────────
# OkHttp ships consumer rules; these just silence harmless platform warnings.
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**

# ── androidx.security (Tink) ────────────────────────────────────────────────
# Tink ships consumer keep rules; these silence references to OPTIONAL
# integrations (google-http-client, joda-time) that are not on the classpath
# and are only reached from code paths this app never uses.
-dontwarn com.google.errorprone.annotations.**
-dontwarn com.google.api.client.http.**
-dontwarn org.joda.time.**
