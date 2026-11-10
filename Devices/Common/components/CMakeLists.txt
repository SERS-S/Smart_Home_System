idf_component_register(
    SRCS
        "wifi_manager/wifi_manager.c"
        "mqtt_manager/mqtt_manager.c"
        "button/button.c"
        "control/control.c"
        "time_sync/time_sync.c"
        "publish_connection/publish_connection.c"

    INCLUDE_DIRS
        "wifi_manager/include"
        "mqtt_manager/include"
        "button/include"
        "control/include"
        "time_sync/include"
        "publish_connection/include"

    REQUIRES
        esp_wifi
        esp_event
        esp_netif
        esp_driver_gpio
        wpa_supplicant
        mqtt
        freertos
        cjson
)