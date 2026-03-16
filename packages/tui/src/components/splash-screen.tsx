import { SPLASH_ART, TAGLINE } from '../theme/splash.js';
import { ironRainTheme } from '../theme/theme.js';

export interface SplashScreenProps {
  version: string;
}

export function SplashScreen(props: SplashScreenProps) {
  return (
    <box flexDirection="column" alignItems="center" paddingTop={1}>
      <text fg={ironRainTheme.brand.primary}>
        <b>{SPLASH_ART}</b>
      </text>
      <text fg={ironRainTheme.brand.accent}>{TAGLINE}</text>
      <text fg={ironRainTheme.chrome.muted} marginTop={1}>
        {props.version}
      </text>
    </box>
  );
}
