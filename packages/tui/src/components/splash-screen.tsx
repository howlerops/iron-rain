import { SPLASH_ART, TAGLINE } from '../theme/splash.js';
import { ironRainTheme } from '../theme/theme.js';

export interface SplashScreenProps {
  version: string;
}

export function SplashScreen(props: SplashScreenProps) {
  return (
    <box flexDirection="column" alignItems="center" paddingTop={1}>
      <text color={ironRainTheme.brand.primary} bold>
        {SPLASH_ART}
      </text>
      <text color={ironRainTheme.brand.accent}>{TAGLINE}</text>
      <text color={ironRainTheme.chrome.muted} marginTop={1}>
        {props.version}
      </text>
    </box>
  );
}
