package dev.muxalot.ui.theme

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Shapes
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/** Colors Material 3 has no slot for: mint accent (fills, text on the page), amber warning. */
class MuxColors(val mint: Color, val onMint: Color, val mintText: Color, val warn: Color)

private val LocalMux = staticCompositionLocalOf { MuxColors(Color.Unspecified, Color.Unspecified, Color.Unspecified, Color.Unspecified) }

object Mux {
    val colors: MuxColors @Composable @ReadOnlyComposable get() = LocalMux.current
}

private val DarkMux = MuxColors(Color(0xFF2ECC9A), Color(0xFF06231B), Color(0xFF2ECC9A), Color(0xFFF5B841))
private val LightMux = MuxColors(Color(0xFF2ECC9A), Color(0xFF06231B), Color(0xFF0F7B5A), Color(0xFFB26A00))

private val Dark = darkColorScheme(
    primary = Color(0xFFA78BFA), onPrimary = Color(0xFF1E1B4B),
    primaryContainer = Color(0xFF3B2D75), onPrimaryContainer = Color(0xFFECE9F5),
    secondary = Color(0xFF2ECC9A), onSecondary = Color(0xFF06231B),
    background = Color(0xFF121016), onBackground = Color(0xFFECE9F5),
    surface = Color(0xFF1B1922), onSurface = Color(0xFFECE9F5),
    surfaceVariant = Color(0xFF23202C), onSurfaceVariant = Color(0xFFA29BB5),
    surfaceContainer = Color(0xFF23202C), surfaceContainerHigh = Color(0xFF23202C), surfaceContainerHighest = Color(0xFF2B2736),
    outline = Color(0xFF3A3547), outlineVariant = Color(0xFF3A3547),
    error = Color(0xFFF2707A),
)

private val Light = lightColorScheme(
    primary = Color(0xFF6D28D9), onPrimary = Color.White,
    primaryContainer = Color(0xFFE4DAFB), onPrimaryContainer = Color(0xFF1C1826),
    secondary = Color(0xFF2ECC9A), onSecondary = Color(0xFF06231B),
    background = Color(0xFFF7F5FB), onBackground = Color(0xFF1C1826),
    surface = Color.White, onSurface = Color(0xFF1C1826),
    surfaceVariant = Color(0xFFF0EDF7), onSurfaceVariant = Color(0xFF5E5872),
    surfaceContainer = Color(0xFFF0EDF7), surfaceContainerHigh = Color(0xFFF0EDF7), surfaceContainerHighest = Color(0xFFE8E3F3),
    outline = Color(0xFFD9D3E8), outlineVariant = Color(0xFFD9D3E8),
    error = Color(0xFFB3261E),
)

private val MuxShapes = Shapes(
    small = RoundedCornerShape(10.dp),
    medium = RoundedCornerShape(16.dp),
    large = RoundedCornerShape(20.dp),
)

// System font; weight and size carry the look.
private val MuxTypography = Typography(
    headlineSmall = TextStyle(fontSize = 24.sp, fontWeight = FontWeight.Bold),
    titleLarge = TextStyle(fontSize = 20.sp, fontWeight = FontWeight.SemiBold),
    titleMedium = TextStyle(fontSize = 16.sp, fontWeight = FontWeight.SemiBold),
    labelLarge = TextStyle(fontSize = 13.sp, fontWeight = FontWeight.Medium),
)

@Composable
fun MuxalotTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    CompositionLocalProvider(LocalMux provides if (dark) DarkMux else LightMux) {
        MaterialTheme(
            colorScheme = if (dark) Dark else Light,
            shapes = MuxShapes,
            typography = MuxTypography,
            content = content,
        )
    }
}
