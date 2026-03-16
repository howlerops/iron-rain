type StyleProps = {
  color?: string;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
  inverse?: boolean;
  strikethrough?: boolean;
};

type LayoutProps = {
  flexDirection?: 'row' | 'column';
  flexGrow?: number;
  flexShrink?: number;
  flexWrap?: 'wrap' | 'nowrap';
  alignItems?: 'flex-start' | 'flex-end' | 'center' | 'stretch';
  justifyContent?:
    | 'flex-start'
    | 'flex-end'
    | 'center'
    | 'space-between'
    | 'space-around';
  gap?: number;
  width?: number | string;
  height?: number | string;
  minWidth?: number;
  minHeight?: number;
  maxWidth?: number;
  maxHeight?: number;
  paddingX?: number;
  paddingY?: number;
  paddingTop?: number;
  paddingBottom?: number;
  paddingLeft?: number;
  paddingRight?: number;
  marginX?: number;
  marginY?: number;
  marginTop?: number;
  marginBottom?: number;
  marginLeft?: number;
  marginRight?: number;
  borderStyle?: 'single' | 'double' | 'round' | 'bold' | 'classic';
  borderColor?: string;
};

type TUIElementProps = StyleProps &
  LayoutProps & {
    children?: any;
  };

declare namespace JSX {
  interface IntrinsicElements {
    box: TUIElementProps;
    text: TUIElementProps;
  }
  type Element = any;
  interface ElementChildrenAttribute {
    children: {};
  }
}
