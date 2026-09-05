import type { GlobalThemeOverrides } from 'naive-ui'

export const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#ec4899', primaryColorHover: '#f061a6', primaryColorPressed: '#db2777', primaryColorSuppl: '#ec4899',
    successColor: '#ec4899', successColorHover: '#f061a6', successColorPressed: '#db2777', successColorSuppl: '#ec4899',
    infoColor: '#a394a3', infoColorHover: '#8f7f8f', infoColorPressed: '#7c6c7c', borderRadius: '10px', borderRadiusSmall: '6px',
  },
  Button: { borderRadiusTiny: '10px', borderRadiusSmall: '10px', borderRadiusMedium: '10px', borderRadiusLarge: '10px' },
  Input: { borderRadius: '10px', borderHover: '1px solid #f061a6', borderFocus: '1px solid #ec4899', boxShadowFocus: 'none', caretColor: '#ec4899' },
  Select: { peers: { InternalSelection: { borderRadius: '10px', borderHover: '1px solid #f061a6', borderFocus: '1px solid #ec4899', boxShadowFocus: 'none', boxShadowActive: 'none', caretColor: '#ec4899' } } },
  Card: { borderRadius: '14px' }, Dialog: { borderRadius: '14px' }, Message: { borderRadius: '10px' }, Dropdown: { borderRadius: '10px' },
}
