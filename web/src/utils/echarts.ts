// echarts 按需注册：只打包用到的图表与组件（体积从 ~1MB 降到 ~300KB）
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent, GraphicComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, GraphicComponent, CanvasRenderer])

export default echarts
